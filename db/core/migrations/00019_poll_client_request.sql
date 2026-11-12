-- Anket oluşturmada tekrar gönderim koruması (issue #150; veri modeli v14
-- "Çevrimdışı yazma"). Duyuru, etkinlik ve tayfadaki gibi
-- UNIQUE(oluşturan, client_request_id).
--
-- Alan isteğe bağlıdır: eski istemciler göndermeden anket açabilir; NULL
-- değerler benzersizlik indeksinde çakışmaz. Hesap silmede created_by NULL
-- olunca kayıt indeksten fiilen düşer.
--
-- Geri alma planı: Down indeksi ve kolonu kaldırır; anketlere dokunmaz.

-- +goose Up
ALTER TABLE polls ADD COLUMN client_request_id uuid;
CREATE UNIQUE INDEX polls_client_request_key ON polls (created_by, client_request_id);

-- +goose Down
DROP INDEX polls_client_request_key;
ALTER TABLE polls DROP COLUMN client_request_id;
