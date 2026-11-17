# Demo tayfaları, Keşfet demo seti ve temizlik

Mağaza incelemesi, ekran görüntüleri ve tanıtımlar için kapalı demo
tayfalarını, Keşfet'i denemek için Keşfet demo setini yazar ve gerektiğinde
temizler (issue #179, #227; [ADR-0023](../adr/0023-demo-verisi.md),
[ADR-0035](../adr/0035-kesfet-herkese-acik-ve-demo-temizligi.md)).

Akış: **seed** (`demo-seed`) → **panel onayı** (sen, Keşfet sekmesi) →
dene → gerekirse **purge** (`demo-purge`).

## Kapalı demo seti (mağaza incelemesi)

Kararların özeti:

- 18 sahte yetişkin hesap (`demo-NN@demo.tayfam.app`, Google kimliği yok,
  giriş yapamaz) ve 4 kapalı tayfa: **Ege Klasikleri Demo** (konvoy),
  **Kazdağı Kampçıları Demo** (kamp, balık), **Nif Yürüyüş Demo** (yürüyüş),
  **Urla Halısaha Demo** (halı saha, oyun). Her türden gelecek tarihli
  etkinlik, katılım yanıtları, plakasız konvoy araçları, kamp malzemesi,
  duyurular (biri sabit), soru-cevap ve faydalı oylar, açık ve gizli anketler,
  kanallar ve mesajlar. Birebir mesaj ve fotoğraf yok.
- Senin hesabın: Ege Klasikleri Demo'da **yönetici**, Kazdağı Kampçıları
  Demo'da **moderatör**, Nif Yürüyüş Demo ve Urla Halısaha Demo'da **üye**.
- Tazeleme komutu yoktur. Komutu ikinci kez çalıştırmak kopya üretmez;
  eksik kalan adımları tamamlar. Eskiyen tayfaları pasif tayfa temizliği
  kendiliğinden arşivler. Silmek için `demo-purge -hepsi` (aşağıda).

## Keşfet demo seti

- 36 ayrı sahte hesap (`demo-21` … `demo-56`) ve Keşfet'e başvurmuş 6 tayfa:
  **Belgrad Ormanı Yürüyüş Demo**, **Moda Sahil Voleybol Demo**, **Abant
  Gölü Kamp Demo**, **Ankara Fotoğraf Yürüyüşü Demo**, **Bodrum Kıyı
  Balıkçıları Demo**, **Kapadokya Klasik Konvoy Demo**. Her birinde 6 üye,
  önümüzdeki haftalarda halka açık yerde buluşan etkinlikler, katılım
  yanıtları, birkaç duyuru.
- Bu tayfalarda **üye değilsin** (Keşfet üyesi olduğun tayfaları göstermez).
- 4 sahte hesap sana arkadaşlık isteği gönderir, araç senin hesabınla kabul
  eder; Belgrad, Moda ve Abant'ta "X ve N arkadaşın" görünür.
- Sen 2 hesabı (Ankara'da), 2 hesap seni (Bodrum'da) engeller: Ankara ve
  Bodrum senin Keşfet'inde **görünmez** (engel kuralı, ADR-0035 §3).
  Kapadokya'da ne arkadaş ne engel var: arkadaşsız öğe.
- Tayfalar normal yoldan başvurur ve **panel onayını bekler**; araç onay
  vermez. Onay sende (aşağıda).

Komut çalışan identity pod'unun içinde çalışır: identity'nin yapılandırması,
anahtarları, `identity_app` veritabanı kullanıcısı ve ağ izinleriyle. Yeni
bir manifest, Job veya secret gerekmez; hiçbir otomasyon komutu çalıştırmaz.

## Ön koşullar

- Bu değişikliği içeren sürüm dağıtılmış olmalı (identity `migrate`
  initContainer'ı migration `00011`'i, core'unki `00029`'u kendisi uygular).
- Keşfet'i denemek için core'da `DISCOVER_ENABLED=true` (prod'da açık,
  `deploy/apps/prod/kustomization.yaml`).
- Uygulamaya Google ile girip kaydını tamamladığın hesabın referans kodu
  (uygulamada Profil → Davet et, `TYF-XXXXXX`). E-posta gerekmez.
- VDS'te kubectl'in prod cluster'ını gösterdiği bir bash terminali
  ([VDS kurulumu](vds-kurulumu.md) adım c).

## Çalıştırma

```sh
export NS=tayfam-prod
REF=TYF-XXXXXX   # kendi referans kodun

# 1. Sürüm ve migration: komutlar tanınıyor mu, migration'lar uygulanmış mı
kubectl -n "$NS" exec deploy/identity -c identity -- /app demo-seed -h
kubectl -n "$NS" exec deploy/identity -c identity -- /app demo-purge -h
kubectl -n "$NS" exec -i tayfam-pg-1 -c postgres -- psql -d identity -tAc \
  "SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1"
kubectl -n "$NS" exec -i tayfam-pg-1 -c postgres -- psql -d core -tAc \
  "SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1"
#    Beklenen: iki kullanım metni (-owner-ref-code; -hepsi, -dry-run),
#    identity için 11 ya da üstü, core için 29 ya da üstü.

# 2. Demo verisini yaz (birkaç dakika sürer)
kubectl -n "$NS" exec deploy/identity -c identity -- \
  /app demo-seed -owner-ref-code "$REF"
```

Beklenen çıktının sonu (kimlikler farklı olur; kapalı set daha önce
yazıldıysa "18 yeni" yerine "36 yeni, 18 zaten vardı"):

```
Demo hesapları: 54 yeni, 0 zaten vardı
<tayfa-id>  Ege Klasikleri Demo  rolünüz: ROLE_ADMIN
<tayfa-id>  Kazdağı Kampçıları Demo  rolünüz: ROLE_MODERATOR
<tayfa-id>  Nif Yürüyüş Demo  rolünüz: ROLE_MEMBER
<tayfa-id>  Urla Halısaha Demo  rolünüz: ROLE_MEMBER

Keşfet demo tayfaları (panelde Keşfet sekmesinden onaylayın; üyesi değilsiniz):
<tayfa-id>  Belgrad Ormanı Yürüyüş Demo  Keşfet: onay bekliyor
<tayfa-id>  Moda Sahil Voleybol Demo  Keşfet: onay bekliyor
<tayfa-id>  Abant Gölü Kamp Demo  Keşfet: onay bekliyor
<tayfa-id>  Ankara Fotoğraf Yürüyüşü Demo  Keşfet: onay bekliyor
<tayfa-id>  Bodrum Kıyı Balıkçıları Demo  Keşfet: onay bekliyor
<tayfa-id>  Kapadokya Klasik Konvoy Demo  Keşfet: onay bekliyor

Arkadaşlarınız: 4, engeller: 4
```

Uygulamada 4 arkadaşlık isteği bildirimi ve kabulü görürsün; bu beklenir.

Komut yarıda kesilirse (bağlantı, zaman aşımı) aynı komutu tekrar çalıştır;
var olanlar atlanır. Bir tayfa için `(salt okunur, atlandı)` yazıyorsa o tayfa
arşivlenmiş ya da askıya alınmıştır; komut ona dokunmaz.

## Panel onayı (Keşfet demo seti)

Platform panelinde **Keşfet** sekmesini aç. Kuyrukta yukarıdaki 6 tayfa
"inceleme bekliyor" durumunda görünür. Her birini bir gerekçeyle (10–1000
karakter, ör. "Keşfet demo seti, meta veri incelendi") **onayla**. Onay
son 5 dakikada giriş ister; süre geçtiyse panele yeniden gir.

Onaydan sonra uygulamada Keşfet'te beklenen:

- Belgrad, Moda, Abant: "X ve N arkadaşın" ile en üstte.
- Kapadokya: arkadaş bilgisi olmadan, başlangıç tarihine göre.
- Ankara ve Bodrum: **görünmez** (engel). Başka bir hesapla bakınca görünür.
- Her birine "Katıl" ile istek gönderilebilir; istek tayfanın demo
  kurucusunda bekler (onaylayan yoktur). Denedikten sonra purge temizler.

## Kontrol

```sh
# 54 demo hesabı (kapalı set 18 + Keşfet seti 36), hepsi işaretli
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

## Temizlik (`demo-purge`)

Normal silme yollarını kullanır: her tayfa demo kurucusunun kendi
hesabıyla silinir (hemen salt okunur ve Keşfet dışı, 72 saat sonra
kalıcı), sahte hesaplar hesap silme akışıyla kapanır (kişisel veri hemen
gider; core ve chat üyelikleri, arkadaşlıkları ve engelleri kaldırır).
Senin hesabınla demo hesapları arasındaki arkadaşlık ve engeller de böylece
düşer; komut senin hesabını kullanmaz. Yalnızca `demo` sağlayıcılı hesaplara
ve onların kurduğu, adında "Demo" geçen tayfalara dokunur.

```sh
export NS=tayfam-prod

# 1. Önce say (hiçbir şey silmez)
kubectl -n "$NS" exec deploy/identity -c identity -- /app demo-purge -dry-run

# 2a. Yalnızca Keşfet demo seti (6 tayfa, 36 hesap)
kubectl -n "$NS" exec deploy/identity -c identity -- /app demo-purge

# 2b. Kapalı mağaza demo tayfaları ve 18 hesabı da (mağaza incelemesi
#     sürüyorsa kullanma: Ege Klasikleri Demo'nun kodu çalışmaz olur)
kubectl -n "$NS" exec deploy/identity -c identity -- /app demo-purge -hepsi -dry-run
kubectl -n "$NS" exec deploy/identity -c identity -- /app demo-purge -hepsi
```

Beklenen çıktı (2a):

```
Kapsam: Keşfet demo seti
<tayfa-id>  Belgrad Ormanı Yürüyüş Demo  silme istendi
...
Tayfa: 6, demo hesabı kapatıldı: 36 (arkadaşlıkları ve engelleri hesapla birlikte düşer)
```

Komut tekrar çalıştırılabilir; ikinci çalıştırma "Tayfa: 0, ... 0" yazar.
Bir tayfa için `salt okunur, atlandı` yazıyorsa tayfa arşivlenmiş ya da
askıdadır; kurucu onu silemez. Hesapları kapandığında aktif üyesi kalmazsa
core onu kendisi silme beklemesine alır; sen üyesiysen (kapalı set) tayfa
ayarlarından silebilirsin. Silinen setle aynı kayıtlar gerekirse
`demo-seed` yeniden yazar (yeni hesaplar ve tayfalar olarak).

```sh
# Kontrol: Keşfet seti temizlendiyse 18 (kapalı set), -hepsi ile 0
kubectl -n "$NS" exec -i tayfam-pg-1 -c postgres -- psql -d identity -tAc \
  "SELECT count(*) FROM auth_identities WHERE provider = 'demo'"
```

## Hata durumları

| Çıktı | Anlamı |
|-------|--------|
| `referans koduyla kayıtlı bir hesap bulunamadı` | Kod yanlış ya da hesabın kaydı tamamlanmamış. Hiçbir şey yazılmadı. |
| `-owner-ref-code zorunludur` | Bayrak eksik. |
| `yapılandırma hatalı` | Komut identity container'ı dışında çalıştırıldı (`-c identity`). |
| `tayfa ...: ... Unavailable` | core ya da chat ayakta değil; `kubectl -n "$NS" get pods` ile bak, sonra tekrar çalıştır. |
| `keşfet başvurusu: ... DISCOVER_TOO_FEW_MEMBERS` | Tayfada 5'ten az aktif üye kaldı; komutu tekrar çalıştır (eksik üyeleri ekler). |
| `engel nedeniyle atlanan arkadaşlık: N` | O demo hesabı ile aranızda elle konmuş bir engel var; hesap atlandı. |
| `demo hesabı ...: hesap demo hesabı değil; dokunulmadı` | Sınır kontrolü devreye girdi; hiçbir şey silinmedi. Çıktıyı mimari oturumuna ilet. |
