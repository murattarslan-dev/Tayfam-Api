-- Yerel Postgres (docker compose) ilk açılışta çalıştırır: identity
-- veritabanını POSTGRES_DB oluşturur, core'unki ve chat'inki burada
-- oluşturulur.
-- Cluster'da veritabanları CloudNativePG Database kaynaklarıyla gelir
-- (deploy/base/postgres.yaml).
CREATE DATABASE core;
CREATE DATABASE chat;
