# Platform yöneticisi ekleme ve kaldırma

Platform yönetim paneline (`admin.tayfam.app`) yalnızca identity
veritabanındaki `platform_admins` tablosunda kaydı olan kullanıcılar girer
(Notion "Platform Yönetim Paneli", erişim katmanı 4; ADR-0008). Bu tabloya
**uygulama içinden kayıt eklenemez**: identity'nin hiçbir sorgusu tabloya
yazmaz (`TestNoPlatformAdminWrites`) ve çalışma zamanı kullanıcısı
`identity_app` yalnızca okuma yetkisi alır (#45 birleşince). Kaydı platform
sahibi, migration sahibi kullanıcıyla tek bir SQL komutuyla ekler.

## Ön koşul

Yönetici olacak kişi Tayfam uygulamasına **Google ile** girmiş ve kaydını
tamamlamış olmalıdır. Panel girişinde aynı Google hesabı kullanılır ve bu
hesabın doğrulanmış e-postası Cloudflare Access'e giriş yapılan e-postayla
aynı olmalıdır.

Kişinin referans kodunu (uygulamada Profil → Davet et, `TYF-XXXXXX`) not al.
Komutta e-posta ya da kişisel veri geçmez; referans kodu yeterlidir.

## Ekleme

```sh
REF=TYF-XXXXXX   # yönetici olacak kişinin referans kodu
kubectl -n tayfam-dev exec -i tayfam-pg-1 -c postgres -- \
  psql -v ON_ERROR_STOP=1 -v ref="$REF" -d identity <<'SQL'
INSERT INTO platform_admins (user_id)
SELECT id FROM users
WHERE ref_code = :'ref' AND deleted_at IS NULL AND registered_at IS NOT NULL
RETURNING user_id, granted_at;
SQL
```

Çıktıda tek satır görünmelidir. `INSERT 0 0` ise referans kodu yanlıştır ya
da hesap silinmiş veya kaydı tamamlanmamıştır.

`granted_by` boş kalır: kaydı platform sahibinin doğrudan eklediği anlamına
gelir.

## Kaldırma

Satır silinince açık panel oturumları bir sonraki istekte kapanır (identity
her istekte tabloyu yeniden okur); admin token'ının 8 saatlik ömrünü
beklemek gerekmez.

```sh
REF=TYF-XXXXXX
kubectl -n tayfam-dev exec -i tayfam-pg-1 -c postgres -- \
  psql -v ON_ERROR_STOP=1 -v ref="$REF" -d identity <<'SQL'
DELETE FROM platform_admins
WHERE user_id = (SELECT id FROM users WHERE ref_code = :'ref')
RETURNING user_id;
SQL
```

Hesap silinirse kullanıcı hemen yönetici sayılmaz; satır 30 gün sonra
`users` satırıyla birlikte silinir.

## Listeleme

```sh
kubectl -n tayfam-dev exec -i tayfam-pg-1 -c postgres -- \
  psql -d identity -c 'SELECT user_id, granted_at, granted_by FROM platform_admins'
```

## Denetim kaydı

Her panel girişi ve panelden yapılan her işlem gerekçesiyle
`platform_audit_logs`'a yazılır. Tablo yalnızca eklenir: güncelleme,
boşaltma ve bir yıldan yeni satırın silinmesi trigger'la reddedilir; bir
yıldan eski satırları identity'nin saklama işi siler (ADR-0008).

## Cloudflare Access ayarı

gateway, Access uygulamasının JWT'sini en fazla 24 saatlik oturum süresiyle
kabul eder. Access uygulamasının "Session Duration" değeri 24 saati
geçmemelidir; panel oturumuyla aynı olması için **8 saat** önerilir.

**Cookie settings → SameSite** `None` (varsayılan) kalmalıdır. Panel
girişinde Google, ID token'ı `admin.tayfam.app/auth/google/callback`'e
siteler arası bir form POST'uyla gönderir (`response_mode=form_post`);
`Lax` ya da `Strict` seçilirse tarayıcı `CF_Authorization` çerezini bu
POST'ta göndermez, Access isteği giriş sayfasına çevirir ve panel girişi
tamamlanmaz. Panelin kendi oturum çerezi her durumda `SameSite=Strict`'tir.

## Google ile panel girişi

Panel Google'ın yönlendirme akışını kullanır (OpenID Connect,
`response_type=id_token`, `response_mode=form_post`); tarayıcıya Google'dan
script yüklenmez ve istemci sırrı yoktur.

- gateway: `ADMIN_GOOGLE_CLIENT_ID` = panelin Google OAuth istemci kimliği
  (gizli değil). Aynı değer identity'nin `GOOGLE_CLIENT_IDS` listesinde
  olmalıdır; identity ID token'ının `aud` alanını bu listeyle doğrular.
- Google Cloud Console → APIs & Services → Credentials → bu Web istemcisi:
  - **Authorized JavaScript origins:** `https://admin.tayfam.app`
  - **Authorized redirect URIs:** `https://admin.tayfam.app/auth/google/callback`

Uygulamanın mevcut Web istemcisi kullanılabilir; ayrı bir istemci açmak
panel girişini uygulamadan ayırır ama identity'nin `GOOGLE_CLIENT_IDS`
listesine bir değer daha ekler.
