//! Accepts the HTTP/2 agent connection Cursor opens itself.
//!
//! Cursor's `node:http2` client does not use `http.proxy`. Server config points
//! `agentn` at this listener. The certificate is signed by the takeover CA,
//! which Cursor already trusts.
use std::{
    net::{IpAddr, Ipv4Addr, SocketAddr},
    sync::Arc,
};

use hyper_util::{
    rt::{TokioExecutor, TokioIo},
    server::conn::auto,
    service::TowerToHyperService,
};
use rcgen::{
    CertificateParams, DistinguishedName, DnType, ExtendedKeyUsagePurpose, KeyPair,
    KeyUsagePurpose, SanType,
};
use tokio::{net::TcpListener, task::JoinSet};
use tokio_rustls::TlsAcceptor;
use tokio_util::sync::CancellationToken;

use crate::{cursor::services::server_config, Error, Result};

use super::ca::LoadedCa;

const PREFERRED_PORT: u16 = 47321;

pub struct AgentEndpoint {
    origin: String,
    stop: CancellationToken,
    task: tokio::task::JoinHandle<()>,
}

impl AgentEndpoint {
    pub fn running(&self) -> bool {
        !self.task.is_finished()
    }

    pub async fn stop(mut self) {
        tracing::info!(origin = %self.origin, "stopping HTTP/2 agent listener");
        server_config::set_agent_origin(None);
        self.stop.cancel();
        let _ = tokio::time::timeout(std::time::Duration::from_secs(5), &mut self.task).await;
    }
}

pub async fn start(ca: LoadedCa, router: axum::Router) -> Result<AgentEndpoint> {
    let listener = bind().await?;
    let address = listener.local_addr()?;
    let origin = format!("https://127.0.0.1:{}", address.port());
    let acceptor = TlsAcceptor::from(Arc::new(tls_config(&ca)?));
    let stop = CancellationToken::new();
    server_config::set_agent_origin(Some(origin.clone()));
    tracing::info!(%origin, "HTTP/2 agent listener ready");
    let task = tokio::spawn(serve(listener, acceptor, router, stop.clone()));
    Ok(AgentEndpoint { origin, stop, task })
}

/// Accepts connections until stopped; open connections end with the listener.
async fn serve(
    listener: TcpListener,
    acceptor: TlsAcceptor,
    router: axum::Router,
    stop: CancellationToken,
) {
    let mut connections = JoinSet::new();
    loop {
        let accepted = tokio::select! {
            () = stop.cancelled() => break,
            accepted = listener.accept() => accepted,
            Some(_) = connections.join_next() => continue,
        };
        let stream = match accepted {
            Ok((stream, _)) => stream,
            Err(error) => {
                tracing::warn!(%error, "HTTP/2 agent listener accept failed");
                tokio::time::sleep(std::time::Duration::from_millis(100)).await;
                continue;
            }
        };
        let acceptor = acceptor.clone();
        let service = TowerToHyperService::new(router.clone());
        connections.spawn(async move {
            let tls = match acceptor.accept(stream).await {
                Ok(tls) => tls,
                Err(error) => {
                    tracing::warn!(%error, "HTTP/2 agent TLS handshake failed");
                    return;
                }
            };
            if let Err(error) = auto::Builder::new(TokioExecutor::new())
                .http2_only()
                .serve_connection(TokioIo::new(tls), service)
                .await
            {
                tracing::debug!(%error, "HTTP/2 agent connection closed");
            }
        });
    }
    connections.shutdown().await;
}

async fn bind() -> Result<TcpListener> {
    let preferred = SocketAddr::from((Ipv4Addr::LOCALHOST, PREFERRED_PORT));
    match TcpListener::bind(preferred).await {
        Ok(listener) => Ok(listener),
        Err(error) => {
            tracing::warn!(%preferred, %error, "preferred HTTP/2 agent port is busy; selecting a random port");
            Ok(TcpListener::bind(SocketAddr::from((Ipv4Addr::LOCALHOST, 0))).await?)
        }
    }
}

fn tls_config(ca: &LoadedCa) -> Result<tokio_rustls::rustls::ServerConfig> {
    let (cert, key) = leaf_certificate(ca)?;
    let provider = Arc::new(tokio_rustls::rustls::crypto::aws_lc_rs::default_provider());
    let mut config = tokio_rustls::rustls::ServerConfig::builder_with_provider(provider)
        .with_safe_default_protocol_versions()
        .map_err(|error| Error::Config(format!("agent TLS protocol versions: {error}")))?
        .with_no_client_auth()
        .with_single_cert(vec![cert], key)
        .map_err(|error| Error::Config(format!("agent TLS server config: {error}")))?;
    config.alpn_protocols = vec![b"h2".to_vec()];
    Ok(config)
}

type LeafCertificate = (
    tokio_rustls::rustls::pki_types::CertificateDer<'static>,
    tokio_rustls::rustls::pki_types::PrivateKeyDer<'static>,
);

fn leaf_certificate(ca: &LoadedCa) -> Result<LeafCertificate> {
    let key_pair = KeyPair::generate()
        .map_err(|error| Error::Config(format!("generate agent TLS key: {error}")))?;
    let mut params = CertificateParams::new(Vec::<String>::new())
        .map_err(|error| Error::Config(format!("agent TLS certificate: {error}")))?;
    let mut name = DistinguishedName::new();
    name.push(DnType::CommonName, "127.0.0.1");
    params.distinguished_name = name;
    params
        .subject_alt_names
        .push(SanType::IpAddress(IpAddr::V4(Ipv4Addr::LOCALHOST)));
    params.key_usages = vec![KeyUsagePurpose::DigitalSignature];
    params.extended_key_usages = vec![ExtendedKeyUsagePurpose::ServerAuth];
    params.use_authority_key_identifier_extension = true;
    // Reissued on every start; a short lifetime limits a leaked key.
    let now = time::OffsetDateTime::now_utc();
    params.not_before = now - time::Duration::days(1);
    params.not_after = now + time::Duration::days(30);
    let certificate = params
        .signed_by(&key_pair, &ca.issuer)
        .map_err(|error| Error::Config(format!("sign agent TLS certificate: {error}")))?;
    let cert = tokio_rustls::rustls::pki_types::CertificateDer::from(certificate.der().to_vec());
    let key = tokio_rustls::rustls::pki_types::PrivateKeyDer::Pkcs8(
        tokio_rustls::rustls::pki_types::PrivatePkcs8KeyDer::from(key_pair.serialize_der()),
    );
    Ok((cert, key))
}

#[cfg(test)]
mod tests {
    use super::*;
    use rcgen::{BasicConstraints, IsCa, Issuer};
    use x509_parser::{extensions::GeneralName, prelude::FromDer};

    fn loaded_ca() -> LoadedCa {
        let key = KeyPair::generate().expect("generate CA key");
        let mut params = CertificateParams::new(Vec::<String>::new()).expect("CA parameters");
        params.is_ca = IsCa::Ca(BasicConstraints::Constrained(0));
        params.key_usages = vec![KeyUsagePurpose::KeyCertSign, KeyUsagePurpose::CrlSign];
        let certificate = params.self_signed(&key).expect("sign CA certificate");
        LoadedCa {
            issuer: Issuer::from_ca_cert_pem(&certificate.pem(), key).expect("parse CA"),
        }
    }

    #[test]
    fn leaf_certificate_is_scoped_to_the_loopback_listener() {
        let ca = loaded_ca();
        let config = tls_config(&ca).expect("build agent TLS config");
        assert_eq!(config.alpn_protocols, vec![b"h2".to_vec()]);
        let (cert, _) = leaf_certificate(&ca).expect("issue leaf certificate");
        let (_, certificate) =
            x509_parser::certificate::X509Certificate::from_der(&cert).expect("parse leaf");
        let san = certificate
            .subject_alternative_name()
            .expect("read SAN extension")
            .expect("SAN extension is present");
        assert!(san
            .value
            .general_names
            .iter()
            .any(|name| matches!(name, GeneralName::IPAddress(ip) if *ip == [127, 0, 0, 1])));
    }
}
