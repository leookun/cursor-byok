//! Enforces Conversation ownership during concurrent writes.
use base64::{engine::general_purpose::STANDARD, Engine};
use sha2::{Digest, Sha256};
use sqlx::{Row, SqliteConnection};

use crate::{Error, Result};

use super::{now_ms, Store};

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct BlobId([u8; 32]);

impl BlobId {
    pub fn digest(data: &[u8]) -> Self {
        Self(Sha256::digest(data).into())
    }

    pub fn from_bytes(bytes: &[u8]) -> Result<Self> {
        let value: [u8; 32] = bytes.try_into().map_err(|_| {
            Error::Protocol(format!("BlobID must be 32 bytes, got {}", bytes.len()))
        })?;
        Ok(Self(value))
    }

    pub fn from_base64(value: &str) -> Result<Self> {
        let decoded = STANDARD
            .decode(value)
            .map_err(|error| Error::Protocol(format!("invalid BlobID base64: {error}")))?;
        Self::from_bytes(&decoded)
    }

    pub fn as_bytes(&self) -> &[u8; 32] {
        &self.0
    }
    pub fn to_base64(&self) -> String {
        STANDARD.encode(self.0)
    }
}

#[derive(Clone, Debug)]
pub struct BlobEdge {
    pub child: BlobId,
    pub field_name: String,
}

impl Store {
    pub async fn put_blob(&self, data: &[u8], edges: &[BlobEdge]) -> Result<BlobId> {
        let blob_id = BlobId::digest(data);
        let mut session = self.write_session().await?;
        let mut tx = session.begin().await?;
        Self::put_blob_tx(&mut tx, &blob_id, data, edges).await?;
        tx.commit().await?;
        session.committed();
        Ok(blob_id)
    }

    pub(crate) async fn put_blob_tx(
        tx: &mut SqliteConnection,
        blob_id: &BlobId,
        data: &[u8],
        edges: &[BlobEdge],
    ) -> Result<()> {
        sqlx::query("INSERT OR IGNORE INTO blobs(blob_id, data, created_at_ms) VALUES (?, ?, ?)")
            .bind(blob_id.as_bytes().as_slice())
            .bind(data)
            .bind(now_ms())
            .execute(&mut *tx)
            .await?;
        for edge in edges {
            sqlx::query(
                "INSERT OR IGNORE INTO blob_edges(parent_blob_id, child_blob_id, field_name) VALUES (?, ?, ?)",
            )
            .bind(blob_id.as_bytes().as_slice())
            .bind(edge.child.as_bytes().as_slice())
            .bind(&edge.field_name)
            .execute(&mut *tx)
            .await?;
        }
        Ok(())
    }

    pub async fn get_blob(&self, blob_id: &BlobId) -> Result<Option<Vec<u8>>> {
        Ok(sqlx::query("SELECT data FROM blobs WHERE blob_id = ?")
            .bind(blob_id.as_bytes().as_slice())
            .fetch_optional(&self.pool)
            .await?
            .map(|row| row.get(0)))
    }

    pub async fn blob_closure(&self, roots: &[BlobId]) -> Result<Vec<BlobId>> {
        let mut seen = std::collections::HashSet::new();
        let mut stack = roots.to_vec();
        while let Some(id) = stack.pop() {
            if !seen.insert(id.clone()) {
                continue;
            }
            let rows = sqlx::query("SELECT child_blob_id FROM blob_edges WHERE parent_blob_id = ?")
                .bind(id.as_bytes().as_slice())
                .fetch_all(&self.pool)
                .await?;
            for row in rows {
                stack.push(BlobId::from_bytes(row.get::<Vec<u8>, _>(0).as_slice())?);
            }
        }
        let mut closure: Vec<_> = seen.into_iter().collect();
        closure.sort_by(|left, right| left.as_bytes().cmp(right.as_bytes()));
        Ok(closure)
    }
}
