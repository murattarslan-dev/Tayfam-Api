-- Demo hesapları (issue #179, ADR-0023). Mağaza incelemesi ve ekran
-- görüntüleri için prod'a yazılan sahte kullanıcılar Google/Apple kimliği
-- taşımaz; giriş kimlikleri "demo" sağlayıcısıyla tutulur. Bu satır hiçbir
-- giriş yolunda eşleşmez (session.SignIn yalnızca google ve apple
-- doğrulayıcılarını kabul eder) ve panel sayılarında demo hesaplarını
-- ayırmanın tek işaretidir: e-posta her zaman <subject>@demo.tayfam.app.

-- +goose Up
ALTER TABLE auth_identities DROP CONSTRAINT auth_identities_provider;
ALTER TABLE auth_identities ADD CONSTRAINT auth_identities_provider
  CHECK (provider IN ('google', 'apple', 'demo'));
-- Demo kimliği yalnızca demo-NN biçiminde ve kendi işaret e-postasıyla.
ALTER TABLE auth_identities ADD CONSTRAINT auth_identities_demo
  CHECK (provider <> 'demo' OR (subject ~ '^demo-[0-9]{2}$' AND email IS NOT NULL AND email = subject || '@demo.tayfam.app'));

-- +goose Down
-- Var olan demo satırları silinmez (ADR-0023: demo verisi silinmez); eski
-- kısıt yalnızca yeni satırlar için geri gelir.
ALTER TABLE auth_identities DROP CONSTRAINT auth_identities_demo;
ALTER TABLE auth_identities DROP CONSTRAINT auth_identities_provider;
ALTER TABLE auth_identities ADD CONSTRAINT auth_identities_provider
  CHECK (provider IN ('google', 'apple')) NOT VALID;
