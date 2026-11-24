# ADR-0044: Yazbozda eşli kip (`side: pair`)

- **Durum:** Kabul edildi (platform sahibi, 2026-11-24, sohbette; issue
  #343). Ürün kararı platform sahibinindir; "Seçenekler" bölümündeki biçim
  seçimleri ve aşağıdaki ayrıntılar bu ADR'de gerekçelendirildi ve PR'da
  platform sahibinin onayına sunuldu.
- **Tarih:** 2026-11-24
- **Karar veren:** Murat Arslan (2026-11-24, sohbette: "masada eşli kip
  olur; 4 kişi oturur, karşılıklı oturanlar eş olur (1–3, 2–4); puan eş
  başına girilir; etkinlikte takımlar bölümü gerekmez; 101 Okey, Batak,
  Pişti, Okey; bireysel kip de kalır").
- **İlgili:** [ADR-0016](0016-desteklenen-istemci-surumu.md),
  [ADR-0021](0021-kvkk-veri-disa-aktarma.md),
  [ADR-0025](0025-etkinlik-turu-katalogu-ve-bilesenler.md) §4, §9, §11,
  [ADR-0037](0037-katalog-ayrintilari-ve-sema-ekleri.md) §5, §6,
  [ADR-0039](0039-rozetler.md),
  [ADR-0042](0042-katalog-degisikligi-olayi.md); Tayfam-App #316
- **Değiştirdiği:** ADR-0037 §5 (ön ayardaki "takım/eşli" kuralı burada
  tanımlanır; ilk ön ayar listesine dört eşli ön ayar eklenir), ADR-0025 §4
  (`scoreboard` bileşeninin `side` parametresi `pair` değerini de alır).

## Bağlam

Yazboz (ADR-0025 §4, ADR-0037 §5) bugün iki taraf bilir: `player` (her yer
bir kişi ya da misafir) ve `team` (her yer etkinliğin bir `teams`
bileşenindeki takım). Okey, 101 Okey, Batak ve Pişti Türkiye'de çoğunlukla
**eşli** oynanır: dört kişi oturur, karşılıklı oturan iki kişi eştir ve
puan eş başına yazılır.

`team` tarafı buna uymaz: takım bileşeni ister, oysa masa oyunu gecesinde
(`board_game_night`) takım kurma bölümü yoktur (Notion §F.1: "türde takım
kurma yok") ve bir gecede masa masa değişen eşler için ayrı takım kurmak
anlamsızdır. Platform sahibi eşli kipi masanın kendi özelliği olarak
istedi.

## Karar

### 1. Kip: üçüncü taraf `pair`, eşli oyunlar ayrı ön ayar

- Kural kümesine (`Rules.Side`, `event_score_tables.side`) üçüncü değer
  eklenir: **`pair`**. `player` ve `team` aynen kalır.
- Katalogda dört yeni ön ayar (migration 00070):

  | Anahtar | Ad | Kurallar |
  |---------|----|----------|
  | `pair_batak` | Batak (eşli) | 4 yer, `pair`, el el, yüksek kazanır |
  | `pair_101` | 101 Okey (eşli) | 4 yer, `pair`, el el, düşük kazanır |
  | `pair_tiles` | Okey (eşli) | 4 yer, `pair`, el el, yüksek kazanır |
  | `pair_pisti` | Pişti (eşli) | 4 yer, `pair`, el el, hedef 151, yüksek kazanır |

  Kurallar bireysel karşılıklarıyla aynıdır; yalnızca taraf ve yer sayısı
  farklıdır. Anahtarlar `pair_` önekiyle ve "okey" sözcüğü geçmeden
  adlandırılır: gitleaks'in genel API anahtarı kuralı "okey" içindeki "key"
  sözcüğünü yakalıyor (yanlış alarm); denetimi gevşetmek yerine ad seçildi
  (platform sahibinin kararı, 2026-11-24). Okey için `pair_tiles` (taş oyunu). Sıra: her eşli ön ayar bireyselinin hemen arkasında (ön ayar
  sıraları 10'un katlarına taşındı, aralarına eşliler girdi).
- `board_game_night` **sürüm 2** ile bu dört ön ayarı da sunar (11 ön
  ayar). Sürüm 1 ile açılmış etkinlikler sürüm 1'in listesiyle kalır
  (ADR-0025 §2); tür sürümü yalnızca ileri gider. Kataloğun bütünü
  değiştiği için `catalog_version` bir artar; `catalog.changed`'i
  `catalog-publish` işi yayınlar (ADR-0042 §5).
- Bireysel kip olduğu gibi kalır: organizatör masayı kurarken "Batak" ya da
  "Batak (eşli)" seçer.
- `side: pair` tür tanımında bileşen parametresi olarak da geçerlidir
  (ileride yalnızca eşli oynanan bir tür için); panelin ön ayar
  doğrulaması da `pair`'i kabul eder. Eşli ön ayarın yer sınırı 4'ü
  dışlayamaz (`players_min` ≤ 4 ≤ `players_max`, verilmişse).

### 2. Koltuk ve eş

- Eşli masada **tam 4 yer** vardır (ya da henüz hiç yer yoktur; yerler
  ilk elden önce verilebilir, bugünkü kural). 4'ten farklı yer sayısı
  `invalid-request` (`players`).
- Yerler listedeki sırayla 1–4 numarayı alır; **eş 1 = koltuk 1 ve 3, eş 2
  = koltuk 2 ve 4** (karşılıklı oturanlar). Eşleri değiştirmek yerlerin
  sırasını değiştirmektir (`PUT …/players`, ilk elden önce).
- Her yer yanıtta `pair` (1 ya da 2) taşır; eşli olmayan masada alan
  yoktur. Eş, konumdan türetilir; ayrıca saklanmaz (iki kaynak olmasın).
- Veritabanı da korur: eşli masada yer numarası 4'ü geçemez, takım yeri
  olamaz (`event_score_players_pair_guard` tetikleyicisi). Tam 4 yer
  kuralı satır kısıtıyla ifade edilemediği için uygulamadadır.

### 3. Tur girişi

- Puan **eş başına** girilir: bir elde her eş için en çok bir skor. Skor,
  eşin koltuklarından **herhangi birine** yazılarak gönderilir
  (`player_id`). Aynı eşin iki koltuğu birlikte gönderilirse değerleri
  aynı olmalıdır (aksi `invalid-request`, `scores`); bu, eşli kipi
  bilmeyen istemcinin dört kutuya aynı değeri yazmasını da kabul eder.
- Sunucu eşin skorunu **iki koltuğa da** yazar (aynı değer, aynı
  transaction'da; yazan tek yol `writeRound`). Böylece el ızgarası,
  dışa aktarma ve eski istemci her kişinin satırında eşinin ortak skorunu
  görür.
- `points` modunda en az bir eş; `wdl` modunda iki eş, birinin galibiyeti
  ve diğerinin mağlubiyeti ya da ikisinin beraberliği (bugünkü kural eş
  düzeyinde).
- Düzeltme ve son eli silme aynen çalışır (el bütün olarak silinip
  yeniden yazılır).

### 4. Toplam, sıralama ve kazanan

- Toplam, kazanılan el ve hedef **eş başına** hesaplanır; eşin iki
  koltuğundaki aynı skor bir kez sayılır (`Compute`, `Decided`).
- `standings` yine **yer başınadır** (4 satır): her yer eşinin toplamını,
  kazandığı eli ve sırasını taşır. Sıra eşler arasında verilir: kazanan
  eşin iki yeri 1., diğer eşin iki yeri 2. (beraberlikte dördü de 1.).
- `winner_ids` biten masada **kazanan eşin iki yeridir** (beraberlikte
  dört yer).

### 5. Kutlama, şampiyonluk rozeti ve ana sayfa

- Kutlama (ADR-0025 §4, #255) yazbozun kazanan yerlerini okuduğu için eşin
  **iki kişisi de kazanandır**; kod değişmez.
- Şampiyonluk rozeti (ADR-0039) kutlamanın kazananlarına verildiği için
  eşin iki kişisi de rozet alır; tayfa ana sayfasının şampiyon kartı da
  ikisini gösterir.

### 6. Misafir oyuncu

- Misafir (ADR-0037 §6) eşli masaya oturabilir, eş de olabilir: eşli masa
  bir kişi masasıdır ve bileşenin `guests` parametresi kuralı aynen
  geçerlidir (`board_game_night`'ta açık). Bir eşin iki kişisi de misafir
  olabilir.
- Misafir yine hiçbir istatistiğe, rozete ve kutlama bildirimine girmez:
  kişi + misafir eşi kazanırsa rozeti yalnızca kişi alır; kutlamada misafir
  takma adıyla görünür.

### 7. İstatistik, dışa aktarma ve hesap silme

- **İstatistik:** yazboz bugün hiçbir istatistik metriğine girmez (ADR-0026;
  ortak metrikler ve ölçüm/günlük metrikleri); eşli kip bunu değiştirmez.
  Arkadaşlık puanı da yazbozu okumaz.
- **Dışa aktarma** (ADR-0021, `core/scores.json`): biçim değişmez. Kişinin
  yerindeki skor eşinin ortak skorudur (iki koltuğa da yazıldığı için).
  Eşin kimliği ve diğer yerler dışa aktarılmaz (bugünkü kural).
- **Hesap silme** (ADR-0025 §11): yer "Silinmiş kullanıcı" olarak kalır
  (`user_id` boşalır), eşin skorları iki koltukta da kalır; masa eşli ve 4
  yerli kalır, sonucu değişmez.

### 8. Eski istemci (0.3.0, ADR-0025 §9)

0.3.0 istemcinin üretilen modeli tanımadığı enum değerini `unknown`
okur (çökmez); `side` `unknown` olan masayı kişi masası gibi çizer. Bu
karar eski istemcide şunu verir:

- **Okuma güncelleme istemez:** yerler, el ızgarası (eşlerin satırında aynı
  skor), sıralama (her kişinin satırında eşin toplamı ve sırası) ve
  kazananlar (iki kişi) doğru görünür. `pair` alanını tanımaz, yok sayar.
- **Masa kurma:** eşli ön ayarlar listede adlarıyla ("Batak (eşli)")
  görünür; kurulan masada eşler seçim sırasından (1–3, 2–4) çıkar ama
  arayüz bunu göstermez. Tam 4 yer kuralı sunucudadır.
- **El girişi:** eşin tek koltuğuna ya da iki koltuğuna aynı değer
  yazılırsa kabul edilir; eşin iki koltuğuna farklı değer yazılırsa genel
  "geçersiz istek" hatası görülür. Veri bozulmaz.
- Bu yüzden en düşük istemci sürümü (ADR-0016) **yükseltilmez** ve
  bileşenin en düşük istemci sürümü (`MinClientVersions`) değişmez:
  güvenlik ya da veri bütünlüğü riski yoktur. Eşli düzen ve eş başına giriş
  Tayfam-App #316 ile gelir.

## Seçenekler

**Kip ve ön ayar biçimi** (issue #343'te açık bırakılan soru):

- **(a) Ayrı eşli ön ayarlar (`pair_batak` …) — seçildi.** Bugünkü
  tasarımda ön ayar bir oyunun kural kümesinin tamamıdır ve masaya kurulurken
  kopyalanır; takımlı oyun da zaten ayrı bir ön ayardır (`team_rounds`,
  `side: team`). Eşli Batak, 4 yerle sınırlı, tarafı farklı bir kural
  kümesidir. Bu biçimde masa kurma isteği, öncelik zinciri (bileşen < ön
  ayar < masanın hedefi) ve ön ayarın yer sınırları hiç değişmez; yeni
  istek alanı, yeni hata türü ya da koşullu sınır yoktur. Ön ayar veridir:
  eşli bir oyun daha (ör. King) panelden kod ve uygulama sürümü olmadan
  eklenir. Bedeli: ön ayar listesi uzar (masa oyunu gecesinde 7 → 11) ve
  tür sürüm 2 alır.
- **(b) Masa açılırken kip seçimi** (istekte `mode: pairs`; ön ayarda
  `pairs_allowed`). Ön ayar listesi kısa kalır, ama ön ayarın yer sınırı
  kipe göre koşullu olur (Batak bireysel 3–4, eşli tam 4), istek ve
  doğrulama yeni bir alan ve kombinasyon kuralı (kip × ön ayar × bileşen
  parametresi) kazanır, ön ayar artık kuralın tamamı olmaz. Daha kırılgan
  bulundu. Tek üstünlüğü eski istemcinin eşli masa kuramamasıdır; eski
  istemcide eşli masanın verisi bozulmadığı için (§8) bu belirleyici
  sayılmadı.
- **(c) Takım bileşeniyle iki kişilik takımlar** (`team_size: 2`).
  Platform sahibinin "takımlar bölümü gerekmez" kararıyla çelişir;
  reddedildi.

**Skorun saklanışı:**

- **İki koltuğa da yazmak — seçildi.** Eski istemci, dışa aktarma ve el
  ızgarası ek mantık olmadan doğru görünür; hesap silinen koltuktan sonra da
  eşin skoru diğer koltukta durur. Tutarlılık tek yazma yolu ve hesapta eş
  başına tek sayım ile korunur.
- **Yalnızca eşin ilk koltuğuna (1 ya da 2) yazmak:** veri tekrarı yok,
  ama eski istemcide koltuk 3 ve 4 boş görünür ve dışa aktarmada kişinin
  skoru yalnızca bir eşte çıkar; reddedildi.
- **Eş başına ayrı tablo/satır (`event_score_pairs`):** yer modeli, dışa
  aktarma, kutlama ve hesap silme yollarında ikinci bir kimlik türü
  açardı; reddedildi.

**Sıra numarası:** eşler arasında 1, 2 (yer bazında rekabet sırası 1, 1, 3
yerine): ikinci eş "üçüncü" değildir.

## Gerekçe

- Eşli kip ön ayar verisine ve masaya kopyalanan kurallara oturur; yeni
  bir kavram (eş) yalnızca koltuk numarasından türetilir.
- Hesap eş başına, sunuş yer başınadır: kutlama, rozet, ana sayfa ve eski
  istemci yer listesini okumaya devam eder; değişiklik yazbozun içinde
  kalır.
- Tutmadığımız veri yoktur: eş ayrıca saklanmaz, yeni kişisel veri
  kategorisi doğmaz (ADR-0011).

## Sonuçlar

- Migration 00070: `event_score_tables_side` CHECK'i `pair`'i kabul eder;
  `event_score_players_pair_guard` tetikleyicisi; dört ön ayar ve ön ayar
  sıraları; `board_game_night` sürüm 2; `catalog_version` +1. Veri
  taşınmaz ve silinmez. Geri alma eşli masaları siler (Down).
- Sözleşme: OpenAPI `EventScoreRules.side` `pair`'i, `EventScorePlayer`
  `pair` alanını alır; proto `ScorePlayer.pair`. Yeni uç ve hata türü
  yoktur.
- `scoreboard` paketi: `SidePair`, `PairSeats`, `Player.Pair`; `Compute` ve
  `Decided` eş başına hesaplar.
- Tayfam-App #316: eşli masa düzeni (eş 1 / eş 2), eş başına tek puan
  kutusu, ön ayar seçiminde eşli ön ayarlar.

## Değerlendirilen alternatifler

"Seçenekler" bölümünde. Ayrıca: eşli kipi yalnızca istemcide göstermek
(sunucu dört kişi masası tutar, istemci toplar) reddedildi: kazanan,
kutlama ve rozet sunucuda hesaplanır (ADR-0025 §5, yetki ve sonuç
sunucudadır).
