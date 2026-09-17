//! Runs embedded SQLite migrations with enough progress data to diagnose startup failures.
use std::{
    borrow::Cow,
    collections::HashSet,
    future::Future,
    path::{Path, PathBuf},
    time::{Duration, Instant},
};

use sqlx::{
    migrate::{Migration, Migrator},
    Row, SqlitePool,
};

use crate::{Error, Result};

const MIGRATION_HEARTBEAT_INTERVAL: Duration = Duration::from_secs(2);
const MIGRATION_STAGE_TIMEOUT: Duration = Duration::from_secs(30);

static ALL_MIGRATIONS: Migrator = sqlx::migrate!("./migrations");

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum MigrationLineEndings {
    Lf,
    Crlf,
}

impl MigrationLineEndings {
    fn label(self) -> &'static str {
        match self {
            Self::Lf => "lf",
            Self::Crlf => "crlf",
        }
    }
}

#[derive(Debug)]
struct AppliedMigration {
    version: i64,
    description: String,
    success: bool,
    checksum: Vec<u8>,
    execution_time_ns: i64,
}

fn migrator_with_line_endings(line_endings: MigrationLineEndings) -> Migrator {
    let migrations = ALL_MIGRATIONS
        .iter()
        .map(|migration| {
            let lf_sql = migration.sql.replace("\r\n", "\n");
            let sql = match line_endings {
                MigrationLineEndings::Lf => lf_sql,
                MigrationLineEndings::Crlf => lf_sql.replace('\n', "\r\n"),
            };
            Migration::new(
                migration.version,
                migration.description.clone(),
                migration.migration_type,
                Cow::Owned(sql),
                migration.no_tx,
            )
        })
        .collect::<Vec<_>>();
    Migrator {
        migrations: Cow::Owned(migrations),
        ..Migrator::DEFAULT
    }
}

fn history_matches(migrator: &Migrator, applied: &[AppliedMigration]) -> bool {
    applied.iter().all(|applied| {
        applied.success
            && migrator.iter().any(|known| {
                known.version == applied.version && known.checksum.as_ref() == applied.checksum
            })
    })
}

fn select_migrator(applied: &[AppliedMigration]) -> (Migrator, MigrationLineEndings) {
    let lf = migrator_with_line_endings(MigrationLineEndings::Lf);
    if history_matches(&lf, applied) {
        return (lf, MigrationLineEndings::Lf);
    }

    let crlf = migrator_with_line_endings(MigrationLineEndings::Crlf);
    if history_matches(&crlf, applied) {
        return (crlf, MigrationLineEndings::Crlf);
    }

    // Preserve SQLx's exact validation error for dirty, unknown, or genuinely modified history.
    (lf, MigrationLineEndings::Lf)
}

pub(super) async fn run(pool: &SqlitePool, database_path: &Path) -> Result<()> {
    tracing::info!("running database migrations with diagnostics");
    log_database_files(database_path);
    log_sqlite_state(pool).await?;

    let applied = load_applied_migrations(pool).await?;
    let (migrator, line_endings) = select_migrator(&applied);
    tracing::info!(
        line_endings = line_endings.label(),
        "selected database migration line endings"
    );
    let known_versions = migrator
        .iter()
        .map(|migration| migration.version)
        .collect::<HashSet<_>>();
    let applied_versions = applied
        .iter()
        .map(|migration| migration.version)
        .collect::<HashSet<_>>();
    let known = migrator
        .iter()
        .map(|migration| format!("{:04} {}", migration.version, migration.description))
        .collect::<Vec<_>>();
    let pending = migrator
        .iter()
        .filter(|migration| !applied_versions.contains(&migration.version))
        .map(|migration| format!("{:04} {}", migration.version, migration.description))
        .collect::<Vec<_>>();

    tracing::info!(known = ?known, "embedded database migrations");
    if applied.is_empty() {
        tracing::info!("no applied database migrations were found");
    } else {
        for migration in &applied {
            tracing::info!(
                version = migration.version,
                description = %migration.description,
                success = migration.success,
                execution_time_ns = migration.execution_time_ns,
                checksum_bytes = migration.checksum.len(),
                "applied database migration"
            );
        }
    }
    tracing::info!(pending = ?pending, pending_count = pending.len(), "pending database migrations");

    let invalid_history = applied.iter().any(|applied| {
        let Some(known) = migrator
            .iter()
            .find(|migration| migration.version == applied.version)
        else {
            return true;
        };
        !applied.success || known.checksum.as_ref() != applied.checksum
    }) || applied
        .iter()
        .any(|migration| !known_versions.contains(&migration.version));

    if invalid_history {
        tracing::error!(
            "database migration history is dirty, unknown, or has a checksum mismatch; running SQLx validation for the exact error"
        );
        return run_stage("migration history validation", migrator.run(pool)).await;
    }

    for (index, migration) in migrator.iter().enumerate() {
        if applied_versions.contains(&migration.version) {
            continue;
        }

        let stage = format!("{:04} {}", migration.version, migration.description);
        let prefix = Migrator {
            migrations: Cow::Owned(migrator.iter().take(index + 1).cloned().collect()),
            ignore_missing: true,
            ..Migrator::DEFAULT
        };
        run_stage(&stage, prefix.run(pool)).await?;
        log_database_files(database_path);
        log_sqlite_state(pool).await?;
    }

    run_stage("final migration history validation", migrator.run(pool)).await?;
    tracing::info!("database migrations completed");
    Ok(())
}

async fn run_stage<F>(stage: &str, future: F) -> Result<()>
where
    F: Future<Output = std::result::Result<(), sqlx::migrate::MigrateError>>,
{
    run_stage_with_limits(
        stage,
        MIGRATION_HEARTBEAT_INTERVAL,
        MIGRATION_STAGE_TIMEOUT,
        future,
    )
    .await
}

async fn run_stage_with_limits<F>(
    stage: &str,
    heartbeat_interval: Duration,
    stage_timeout: Duration,
    future: F,
) -> Result<()>
where
    F: Future<Output = std::result::Result<(), sqlx::migrate::MigrateError>>,
{
    let started = Instant::now();
    let mut heartbeat = tokio::time::interval(heartbeat_interval);
    heartbeat.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
    heartbeat.tick().await;
    let deadline = tokio::time::sleep(stage_timeout);
    tokio::pin!(deadline);
    tokio::pin!(future);

    tracing::info!(
        stage,
        timeout_seconds = stage_timeout.as_secs_f64(),
        "database migration stage started"
    );
    loop {
        tokio::select! {
            result = &mut future => {
                let elapsed_ms = started.elapsed().as_millis();
                return match result {
                    Ok(()) => {
                        tracing::info!(stage, elapsed_ms, "database migration stage completed");
                        Ok(())
                    }
                    Err(error) => {
                        tracing::error!(stage, elapsed_ms, %error, "database migration stage failed");
                        Err(error.into())
                    }
                };
            }
            _ = heartbeat.tick() => {
                tracing::warn!(
                    stage,
                    elapsed_ms = started.elapsed().as_millis(),
                    "database migration stage is still running; the database may be locked by another process"
                );
            }
            _ = &mut deadline => {
                let elapsed_seconds = started.elapsed().as_secs();
                tracing::error!(stage, elapsed_seconds, "database migration stage timed out");
                return Err(Error::MigrationTimeout {
                    stage: stage.to_owned(),
                    timeout_seconds: stage_timeout.as_secs(),
                });
            }
        }
    }
}

async fn load_applied_migrations(pool: &SqlitePool) -> Result<Vec<AppliedMigration>> {
    tracing::info!("reading database migration history");
    let table_exists: i64 = sqlx::query_scalar(
        "SELECT EXISTS(
            SELECT 1 FROM sqlite_master
            WHERE type = 'table' AND name = '_sqlx_migrations'
         )",
    )
    .fetch_one(pool)
    .await?;
    if table_exists == 0 {
        tracing::info!("database migration history table does not exist");
        return Ok(Vec::new());
    }

    let rows = sqlx::query(
        "SELECT version, description, success, checksum, execution_time
         FROM _sqlx_migrations ORDER BY version",
    )
    .fetch_all(pool)
    .await?;
    tracing::info!(row_count = rows.len(), "database migration history loaded");
    rows.into_iter()
        .map(|row| {
            Ok(AppliedMigration {
                version: row.try_get("version")?,
                description: row.try_get("description")?,
                success: row.try_get("success")?,
                checksum: row.try_get("checksum")?,
                execution_time_ns: row.try_get("execution_time")?,
            })
        })
        .collect::<std::result::Result<Vec<_>, sqlx::Error>>()
        .map_err(Into::into)
}

async fn log_sqlite_state(pool: &SqlitePool) -> Result<()> {
    tracing::info!(
        pool_size = pool.size(),
        pool_idle = pool.num_idle(),
        "reading SQLite runtime state"
    );
    let sqlite_version: String = sqlx::query_scalar("SELECT sqlite_version()")
        .fetch_one(pool)
        .await?;
    let journal_mode: String = sqlx::query_scalar("PRAGMA journal_mode")
        .fetch_one(pool)
        .await?;
    let locking_mode: String = sqlx::query_scalar("PRAGMA locking_mode")
        .fetch_one(pool)
        .await?;
    let foreign_keys: i64 = sqlx::query_scalar("PRAGMA foreign_keys")
        .fetch_one(pool)
        .await?;
    let busy_timeout_ms: i64 = sqlx::query_scalar("PRAGMA busy_timeout")
        .fetch_one(pool)
        .await?;
    let schema_version: i64 = sqlx::query_scalar("PRAGMA schema_version")
        .fetch_one(pool)
        .await?;
    let user_version: i64 = sqlx::query_scalar("PRAGMA user_version")
        .fetch_one(pool)
        .await?;
    let page_size: i64 = sqlx::query_scalar("PRAGMA page_size")
        .fetch_one(pool)
        .await?;
    let page_count: i64 = sqlx::query_scalar("PRAGMA page_count")
        .fetch_one(pool)
        .await?;
    let freelist_count: i64 = sqlx::query_scalar("PRAGMA freelist_count")
        .fetch_one(pool)
        .await?;

    tracing::info!(
        %sqlite_version,
        %journal_mode,
        %locking_mode,
        foreign_keys,
        busy_timeout_ms,
        schema_version,
        user_version,
        page_size,
        page_count,
        freelist_count,
        "SQLite runtime state"
    );
    Ok(())
}

fn log_database_files(database_path: &Path) {
    tracing::info!(
        database_path = %database_path.display(),
        process_id = std::process::id(),
        "SQLite database files"
    );
    log_file("database", database_path);
    log_file("wal", &sidecar_path(database_path, "-wal"));
    log_file("shm", &sidecar_path(database_path, "-shm"));
    log_file("journal", &sidecar_path(database_path, "-journal"));
}

fn log_file(kind: &str, path: &Path) {
    match std::fs::metadata(path) {
        Ok(metadata) => tracing::info!(
            kind,
            path = %path.display(),
            exists = true,
            size_bytes = metadata.len(),
            readonly = metadata.permissions().readonly(),
            "SQLite file state"
        ),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => tracing::info!(
            kind,
            path = %path.display(),
            exists = false,
            "SQLite file state"
        ),
        Err(error) => {
            tracing::warn!(kind, path = %path.display(), %error, "failed to read SQLite file state")
        }
    }
}

fn sidecar_path(database_path: &Path, suffix: &str) -> PathBuf {
    let mut path = database_path.as_os_str().to_os_string();
    path.push(suffix);
    PathBuf::from(path)
}

#[cfg(test)]
mod tests {
    use sqlx::sqlite::SqlitePoolOptions;

    use super::*;

    #[test]
    fn normalized_checksums_match_the_published_lf_and_windows_crlf_migrations() {
        let lf = migrator_with_line_endings(MigrationLineEndings::Lf);
        let crlf = migrator_with_line_endings(MigrationLineEndings::Crlf);
        let lf_initial = lf.iter().find(|migration| migration.version == 1).unwrap();
        let crlf_initial = crlf
            .iter()
            .find(|migration| migration.version == 1)
            .unwrap();

        assert_eq!(
            hex::encode(lf_initial.checksum.as_ref()),
            "ddf1bc573e460bfd93ea50b60d003e8fc6bb9a1b32de71139cb0fc0d898e88c401c08c8a8b57abbe68f55927e7f004d9"
        );
        assert_eq!(
            hex::encode(crlf_initial.checksum.as_ref()),
            "7c5995693dbd5f9d50880fc874784cb67c499762abcee4a562b54c9afd8239bae125074696b78f90aca0fc136b802a2b"
        );
    }

    #[tokio::test]
    async fn lf_and_crlf_migration_histories_upgrade_without_rewriting_checksums() {
        for line_endings in [MigrationLineEndings::Lf, MigrationLineEndings::Crlf] {
            let pool = SqlitePoolOptions::new()
                .max_connections(1)
                .connect("sqlite::memory:")
                .await
                .unwrap();
            let historical = migrator_with_line_endings(line_endings);
            let first_four = Migrator {
                migrations: Cow::Owned(historical.iter().take(4).cloned().collect()),
                ..Migrator::DEFAULT
            };
            first_four.run(&pool).await.unwrap();
            let checksum_before: Vec<u8> =
                sqlx::query_scalar("SELECT checksum FROM _sqlx_migrations WHERE version = 1")
                    .fetch_one(&pool)
                    .await
                    .unwrap();

            run(&pool, Path::new("line-ending-compatibility.db"))
                .await
                .unwrap();

            let checksum_after: Vec<u8> =
                sqlx::query_scalar("SELECT checksum FROM _sqlx_migrations WHERE version = 1")
                    .fetch_one(&pool)
                    .await
                    .unwrap();
            let versions: Vec<i64> =
                sqlx::query_scalar("SELECT version FROM _sqlx_migrations ORDER BY version")
                    .fetch_all(&pool)
                    .await
                    .unwrap();
            let checkpoint_table_exists: i64 = sqlx::query_scalar(
                "SELECT EXISTS(
                    SELECT 1 FROM sqlite_master
                    WHERE type = 'table' AND name = 'conversation_checkpoints'
                 )",
            )
            .fetch_one(&pool)
            .await
            .unwrap();

            let argument_error_column_exists: i64 = sqlx::query_scalar(
                "SELECT EXISTS(
                    SELECT 1 FROM pragma_table_info('tool_round_calls')
                    WHERE name = 'argument_error'
                 )",
            )
            .fetch_one(&pool)
            .await
            .unwrap();

            let model_enabled_column_exists: i64 = sqlx::query_scalar(
                "SELECT EXISTS(
                    SELECT 1 FROM pragma_table_info('model_configs')
                    WHERE name = 'enabled'
                 )",
            )
            .fetch_one(&pool)
            .await
            .unwrap();

            assert_eq!(checksum_after, checksum_before);
            assert_eq!(versions, vec![1, 2, 3, 4, 5, 6, 7, 8, 9, 10]);
            assert_eq!(checkpoint_table_exists, 1);
            assert_eq!(argument_error_column_exists, 1);
            assert_eq!(model_enabled_column_exists, 1);
        }
    }

    #[test]
    fn sqlite_sidecar_paths_preserve_the_database_path() {
        let database = Path::new(r"C:\Users\Test User\cursor-byok.db");
        assert_eq!(
            sidecar_path(database, "-wal"),
            PathBuf::from(r"C:\Users\Test User\cursor-byok.db-wal")
        );
    }

    #[tokio::test]
    async fn stalled_migration_stage_returns_a_timeout_error() {
        let result = run_stage_with_limits(
            "0006 rename revisions to checkpoints",
            Duration::from_millis(2),
            Duration::from_millis(10),
            std::future::pending(),
        )
        .await;

        assert!(matches!(
            result,
            Err(Error::MigrationTimeout { stage, .. })
                if stage == "0006 rename revisions to checkpoints"
        ));
    }
}
