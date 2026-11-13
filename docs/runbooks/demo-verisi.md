# Demo tayfaları ve içerik (bir kez)

Mağaza incelemesi, ekran görüntüleri ve tanıtımlar için kapalı demo
tayfalarını ve içeriklerini yazar (issue #179,
[ADR-0023](../adr/0023-demo-verisi.md)). Kararların özeti:

- 18 sahte yetişkin hesap (`demo-NN@demo.tayfam.app`, Google kimliği yok,
  giriş yapamaz) ve 4 kapalı tayfa: **Ege Klasikleri Demo** (konvoy),
  **Kazdağı Kampçıları Demo** (kamp, balık), **Nif Yürüyüş Demo** (yürüyüş),
  **Urla Halısaha Demo** (halı saha, oyun). Her türden gelecek tarihli
  etkinlik, katılım yanıtları, plakasız konvoy araçları, kamp malzemesi,
  duyurular (biri sabit), soru-cevap ve faydalı oylar, açık ve gizli anketler,
  kanallar ve mesajlar. Birebir mesaj ve fotoğraf yok.
- Senin hesabın: Ege Klasikleri Demo'da **yönetici**, Kazdağı Kampçıları
  Demo'da **moderatör**, Nif Yürüyüş Demo ve Urla Halısaha Demo'da **üye**.
- Tazeleme ve silme komutu yoktur. Komutu ikinci kez çalıştırmak kopya
  üretmez; eksik kalan adımları tamamlar. Eskiyen tayfaları pasif tayfa
  temizliği kendiliğinden arşivler.

Komut çalışan identity pod'unun içinde çalışır: identity'nin yapılandırması,
anahtarları, `identity_app` veritabanı kullanıcısı ve ağ izinleriyle. Yeni
bir manifest, Job veya secret gerekmez; hiçbir otomasyon komutu çalıştırmaz.

## Ön koşullar

- Bu değişikliği içeren identity sürümü dağıtılmış olmalı (identity
  `migrate` initContainer'ı migration `00011`'i kendisi uygular).
- Uygulamaya Google ile girip kaydını tamamladığın hesabın referans kodu
  (uygulamada Profil → Davet et, `TYF-XXXXXX`). E-posta gerekmez.
- VDS'te kubectl'in prod cluster'ını gösterdiği bir bash terminali
  ([VDS kurulumu](vds-kurulumu.md) adım c).

## Çalıştırma

```sh
export NS=tayfam-prod
REF=TYF-XXXXXX   # kendi referans kodun

# 1. Sürüm ve migration: komut tanınıyor mu, demo sağlayıcısı var mı
kubectl -n "$NS" exec deploy/identity -c identity -- /app demo-seed -h
kubectl -n "$NS" exec -i tayfam-pg-1 -c postgres -- psql -d identity -tAc \
  "SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1"
#    Beklenen: kullanım metni (-owner-ref-code) ve 11 ya da üstü.

# 2. Demo verisini yaz (birkaç dakika sürer)
kubectl -n "$NS" exec deploy/identity -c identity -- \
  /app demo-seed -owner-ref-code "$REF"
```

Beklenen çıktının sonu (kimlikler farklı olur):

```
Demo hesapları: 18 yeni, 0 zaten vardı
<tayfa-id>  Ege Klasikleri Demo  rolünüz: ROLE_ADMIN
<tayfa-id>  Kazdağı Kampçıları Demo  rolünüz: ROLE_MODERATOR
<tayfa-id>  Nif Yürüyüş Demo  rolünüz: ROLE_MEMBER
<tayfa-id>  Urla Halısaha Demo  rolünüz: ROLE_MEMBER
```

Komut yarıda kesilirse (bağlantı, zaman aşımı) aynı komutu tekrar çalıştır;
var olanlar atlanır. Bir tayfa için `(salt okunur, atlandı)` yazıyorsa o tayfa
arşivlenmiş ya da askıya alınmıştır; komut ona dokunmaz.

## Kontrol

```sh
# 18 demo hesabı, hepsi işaretli
kubectl -n "$NS" exec -i tayfam-pg-1 -c postgres -- psql -d identity -tAc \
  "SELECT count(*) FROM auth_identities WHERE provider = 'demo' AND email LIKE '%@demo.tayfam.app'"
```

Uygulamada Tayfa sekmesinde dört demo tayfası görünür. Sahte hesapların
adları core ve chat'e outbox ile birkaç saniye içinde ulaşır; ilk dakikada
bir ad eksik görünürse yenile.

## Mağaza incelemesi

İnceleme ekibine **Ege Klasikleri Demo**'nun katılım kodunu ver: kodu
yönetici olduğun için uygulamada tayfa ayarlarında görürsün. İnceleme hesabı
kodu girince katılım isteği oluşur; isteği uygulamadan onaylarsın.

## Panel sayıları

Demo hesapları panelin toplam kullanıcı sayısına dahildir. Ayırmak için:
hesaplarda `auth_identities.provider = 'demo'` (e-posta
`@demo.tayfam.app`), tayfalarda adındaki "Demo".

## Hata durumları

| Çıktı | Anlamı |
|-------|--------|
| `referans koduyla kayıtlı bir hesap bulunamadı` | Kod yanlış ya da hesabın kaydı tamamlanmamış. Hiçbir şey yazılmadı. |
| `-owner-ref-code zorunludur` | Bayrak eksik. |
| `yapılandırma hatalı` | Komut identity container'ı dışında çalıştırıldı (`-c identity`). |
| `tayfa ...: ... Unavailable` | core ya da chat ayakta değil; `kubectl -n "$NS" get pods` ile bak, sonra tekrar çalıştır. |
