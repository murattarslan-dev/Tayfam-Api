-- Yerel Postgres (docker compose) ilk açılışta çalıştırır: identity
-- veritabanını POSTGRES_DB oluşturur, core'unki ve chat'inki burada
-- oluşturulur. Cluster'da veritabanları ve kullanıcılar CloudNativePG ile
-- gelir (deploy/base/postgres.yaml).
--
-- Cluster'daki gibi servis başına iki kullanıcı (ADR-0015): <servis>
-- veritabanının sahibidir ve migration'ları çalıştırır, <servis>_app servisin
-- çalışma zamanı kullanıcısıdır; yetkilerini migration'lar verir. Parola
-- yalnızca yerel, atılabilir bir veritabanı içindir.
CREATE ROLE identity LOGIN PASSWORD 'yerel';
CREATE ROLE identity_app LOGIN PASSWORD 'yerel';
CREATE ROLE core LOGIN PASSWORD 'yerel';
CREATE ROLE core_app LOGIN PASSWORD 'yerel';
CREATE ROLE chat LOGIN PASSWORD 'yerel';
CREATE ROLE chat_app LOGIN PASSWORD 'yerel';
ALTER DATABASE identity OWNER TO identity;
CREATE DATABASE core OWNER core;
CREATE DATABASE chat OWNER chat;
