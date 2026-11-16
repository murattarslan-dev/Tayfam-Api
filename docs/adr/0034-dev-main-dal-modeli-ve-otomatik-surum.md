# ADR-0034: dev/main dal modeli ve otomatik sürüm

- **Durum:** Önerildi (platform sahibinin birleştirmesiyle kabul edilir)
- **Tarih:** 2026-11-16
- **Karar veren:** Murat Arslan (2026-11-16 sohbet kararları, mimari
  oturumunun aktarımıyla). Bu ADR onların uygulanmasıdır; açık bırakılan
  seçimler "Açık sorular"dadır.
- **İlgili:** [ADR-0006](0006-dagitim-ve-ortamlar.md),
  [ADR-0010](0010-repo-kurallari-ve-is-akisi.md),
  [ADR-0011](0011-guvenlik-ilkesi-ihlal-varsayimi.md)
- **Değiştirdiği:** ADR-0006 "Image'lar ve dağıtım" (dev'in kaynağı, prod
  sürümünün kim tarafından atıldığı) ve ADR-0010 "Dal ve birleştirme
  kuralları", "Dal koruması", "Sürümleme" bölümleri. İkisine revizyon notu
  eklendi.

## Bağlam

Bugüne kadar tek dal vardı: her iş PR'ı `main`'e squash ile birleşiyor,
`main`'e her birleşme ev cluster'ına ve (yayına kadar dev olarak kullanılan,
ADR-0006 revizyon 2026-11-12) VDS'e dağıtılıyordu. prod sürümünü platform
sahibi elle `vX.Y.Z` etiketiyle atıyordu.

Platform sahibi 2026-11-16'da şunlara karar verdi:

1. Bütün iş PR'ları `dev` dalına squash ile birleşir. `dev`'e birleşmede
   hiçbir dağıtım yoktur; yalnızca CI çalışır.
2. `main` = prod. `dev` → `main` bir **sürüm PR'ı** ile birleşir. `main`'e her
   birleşme prod'a gider ve sürüm etiketi (`v*`) otomatik atılır.
3. Sürüm PR'ını mimari oturumu, platform sahibinin sohbetteki onayıyla
   birleştirir.
4. `main`'e gelen PR yalnızca `dev`'den olabilir.
5. Bugünkü "main'e birleşince dev ortamına dağıtım" davranışının yeni
   modeldeki yeri bu ADR'de önerilir.

Güvenlik kısıtı (ADR-0011): prod artifact'ının imza kimliği bugün
`prod.yml@refs/tags/v*`'dır. `GITHUB_TOKEN` ile atılan bir etiketin push olayı
başka iş akışlarını tetiklemez. Otomatik etiket, PAT veya uzun ömürlü bir
token eklemeden ve imza zincirini bozmadan çalışmalıdır.

## Karar

### Dallar

| Dal | Ne girer | Nasıl | Ne olur |
|-----|----------|-------|---------|
| `dev` | Bütün iş PR'ları (Dependabot dahil) | squash | Yalnızca CI. Image gönderilmez, imza atılmaz, hiçbir ortama dağıtılmaz. |
| `main` | Yalnızca `dev`'den sürüm PR'ı | merge commit | CI → image'lar (imzalı) → dev ortamı → otomatik `vX.Y.Z` → prod |

- `dev` reponun **varsayılan dalıdır**: yeni PR'lar, Dependabot ve
  `Closes #N` ile issue kapatma kendiliğinden `dev`'e göre çalışır.
- Hassas alan kuralları (ADR-0010, CODEOWNERS) **`dev`'e birleşmede**
  uygulanır: `.github/`, `deploy/clusters/`, `.sops.yaml`,
  `scripts/new-signing-key.sh` ve `CLAUDE.md`'ye dokunan PR'ı `dev`'e yine
  yalnızca platform sahibi birleştirir. Sürüm PR'ı bu içeriği yeniden
  incelemez; zaten doğru kişi tarafından `dev`'e alınmış değişiklikleri
  prod'a taşır.
- `main`'e doğrudan iş PR'ı açılmaz; acil düzeltme de önce `dev`'e, sonra
  sürüm PR'ıyla `main`'e gider.

### Sürüm PR'ı merge commit ile birleşir

Sürüm PR'ı squash ile birleşirse `main`'de `dev`'de olmayan yeni bir commit
oluşur; bir sonraki sürüm PR'ı aynı değişiklikleri tekrar gösterir ve
çakışmalar başlar. Bunu önlemek için ya her sürümden sonra `main`'i `dev`'e
geri birleştirmek (korunan dala ek bir PR) ya da merge commit gerekir.

**Merge commit seçildi:** `dev`'in bütün geçmişi `main`'in atası olur;
`main`'de `dev`'de olmayan tek şey içerik taşımayan merge commit'lerdir. Bir
sonraki sürüm PR'ı yalnızca yeni commit'leri gösterir, geri birleştirme
gerekmez. `dev`'deki commit'ler zaten squash edilmiş iş PR'larıdır; `main`'in
geçmişi okunur kalır (her sürüm bir merge commit, altında o sürümün PR'ları).

Sonuçları:

- `main` kural setinde birleştirme yöntemi yalnızca **merge**; `dev`'de
  yalnızca **squash**. Repo ayarında ikisi de açık, rebase kapalı.
- `main` kural setinde **"Require linear history"** ve **"Require branches
  to be up to date before merging"** kapalı olmalıdır. İkincisi açık olursa
  GitHub `dev`'in `main`'deki merge commit'i içermesini ister; bunu
  sağlamanın tek yolu korunan `dev`'e doğrudan push'tur ve kural seti
  bunu reddeder.
- prod iş akışı etiketin `main`'in **birinci ebeveyn zincirinde** olduğunu
  zaten doğruluyor: merge commit'ler bu zincirdedir, `dev`'in ara
  commit'leri (ikinci ebeveyn) değildir. Ara commit'lerin image'ları `main`'de
  derlenip imzalanmadığı için onlara atılan etiket zaten düşer.

### main'e yalnızca dev'den PR

GitHub kural setleri PR'ın kaynak dalını sınırlayamaz. Ayrı bir iş akışı
(`.github/workflows/surum-pr.yml`, iş adı **"Sürüm PR'ı kaynağı"**) `main`'e
açılan her PR'da kaynağın bu reponun `dev` dalı olduğunu kontrol eder;
değilse düşer. `main` kural setinde zorunlu kontroldür.

- Fork'tan gelen ve dalı `dev` adını taşıyan PR, depo adı da
  karşılaştırıldığı için düşer.
- İş akışı `edited` olayını da dinler: hedef dalı sonradan `main`'e
  çevrilen bir PR'da kontrol yeniden çalışır. Ayrı dosyada olması, başlık
  düzenlemelerinin bütün CI'ı yeniden başlatmasını önler.
- Kod checkout edilmez, token'a hiçbir izin verilmez (`permissions: {}`).
- **Geçiş:** `dev` dalı henüz yoksa kontrol uyarıyla geçer (bu ADR'yi getiren
  PR'ın kendisi). `dev` silinmeye karşı kural setiyle korunduğu için bu yol
  geçişten sonra kullanılamaz.

### Otomatik etiket ve imza zinciri

`main`'e push'ta CI şu sırayla çalışır: kontroller → image'lar
(gönderilir, `ci.yml@refs/heads/main` ile imzalanır) → **Dev dağıtımı**
(`dev` artifact'ı) → **Sürüm etiketi**.

"Sürüm etiketi" işi:

1. Commit zaten bir `vX.Y.Z` etiketi taşıyorsa (platform sahibi elle attıysa,
   onun push'u prod iş akışını zaten başlattı) hiçbir şey yapmaz.
2. Yoksa bir sonraki sürüm numarasını hesaplar, etiketi REST API ile
   (`POST /git/refs`) commit'e atar.
3. Prod iş akışını **o etiket ref'inde** `workflow_dispatch` ile başlatır
   (`gh workflow run prod.yml --ref vX.Y.Z`).

Neden çalışır: `GITHUB_TOKEN` ile tetiklenen olaylar yeni iş akışı
başlatmaz; **`workflow_dispatch` ve `repository_dispatch` bu kuralın
belgelenmiş istisnasıdır.** Etiket ref'inde başlatılan prod iş akışının OIDC
token'ındaki `job_workflow_ref` ve dolayısıyla cosign sertifikasının kimliği
`…/.github/workflows/prod.yml@refs/tags/vX.Y.Z` olur; elle atılan etiketin
push'uyla başlayan çalışmayla **aynıdır**.

Bu yüzden:

- PAT, GitHub App anahtarı ya da yeni bir secret **eklenmez**.
- prod iş akışının doğrulamaları değişmez: etiket biçimi, etiketin
  `main`'in birinci ebeveyn zincirinde olması, dört image'ın o commit için
  `ci.yml@refs/heads/main` tarafından imzalanmış olması (`--certificate-github-workflow-sha`),
  secret referanslarının tam olması.
- GitHub, `workflow_dispatch`'in çalışması için iş akışı dosyasının
  varsayılan dalda da bu tetikleyiciyle bulunmasını ister. `dev` varsayılan
  dal olduğu için `prod.yml`'deki `workflow_dispatch` `dev`'de de kalmalıdır;
  kaldırılırsa otomatik sürüm düşer.
- **`deploy/clusters/` bu ADR ile değişmez.** VDS yayına kadar `dev`
  artifact'ını ve `ci.yml@refs/heads/main` imzasını izler (ADR-0006 revizyon
  2026-11-12); yayın öncesi `prod` etiketine ve `prod.yml@refs/tags/v*`
  kimliğine dönüşü planlandığı gibi kalır ve otomatik etiketle de çalışır.

Değerlendirilen ve seçilmeyen yollar:

- **prod.yml'i `ci.yml`'den yeniden kullanılabilir iş akışı
  (`workflow_call`) olarak çağırmak:** sertifika kimliği
  `prod.yml@refs/heads/main` olur; Flux'taki kimlik değişir ve dev artifact'ının
  kimliğiyle aynı ref'e düşer. Reddedildi.
- **`workflow_run` ile tetiklemek:** varsayılan dalda (artık `dev`) çalışır,
  kimlik `prod.yml@refs/heads/dev` olur. Reddedildi.
- **PAT veya GitHub App ile etiket atmak:** uzun ömürlü bir kimlik bilgisi
  ekler; çalınırsa etiket ve push yetkisi verir. ADR-0011 (en az yetki) ile
  çelişir.

### Sürüm numarası

**Son etiketin yaması bir artırılır:** en yüksek `vX.Y.Z` (sürüm sıralamasıyla)
→ `vX.Y.(Z+1)`; hiç etiket yoksa `v0.1.0`. Bugün son etiket `v0.1.6`'dır;
ilk otomatik sürüm `v0.1.7` olur.

- Biçim mevcut kuralla (`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
  ve Flux/prod iş akışının beklentisiyle aynı kalır.
- minor/major artışını platform sahibi elle yapar: `main`'in son commit'ine
  (birinci ebeveyn zincirinde) `vX.(Y+1).0` etiketini atar. Bu aynı içeriği
  yeniden yayınlar (zararsız) ve sonraki otomatik sürümler oradan devam eder.
- **Tarih tabanlı numara reddedildi:** `vYYYY.MM.DD` aylarda ve günlerde
  baştaki sıfır nedeniyle mevcut biçim kuralına uymaz, aynı gün iki sürüm
  için yine bir sayaç gerekir.
- `main` CI çalışmaları aynı eşzamanlılık grubunda sırayla biter
  (`cancel-in-progress: false`), etiketler commit sırasıyla artar. Kuyrukta
  bekleyen bir çalışma daha yenisi gelince iptal edilirse o commit sürüm
  almaz; bir sonraki sürüm onu da içerir.

### Dev ortamı

**Seçilen:** `main`'de önce dev ortamı, ardından prod. `dev` dalına
birleşme dağıtım yapmaz (karar 1). `main`'e her birleşmede "Dev dağıtımı" işi
bugünkü gibi `dev` artifact'ını yayınlar; ev cluster'ı onu alır. "Sürüm
etiketi" bu işe bağlıdır: dev artifact'ı yayınlanamazsa prod sürümü de
başlamaz.

Sonuç: dev ortamı artık her iş PR'ını değil, her sürümü görür; prod'la aynı
commit'i birkaç dakika önce alır. Ayrı bir hazırlık (staging) ortamı
değildir. Kubernetes'e özgü davranış denemek için dev ortamına sürümden önce
dağıtım gerekirse aşağıdaki seçenek (Açık sorular 2) ayrıca karara bağlanır.

**Seçilmeyen:** dev ortamını yalnızca elle (`workflow_dispatch`) `dev`
dalından dağıtmak. İmza kimliği `ci.yml@refs/heads/dev` olacağından
`deploy/clusters/home`'daki doğrulama kimliğinin değişmesini gerektirir
(yalnızca platform sahibi); ev bilgisayarı gün içinde kapalı olduğu için
şimdilik değeri düşük.

### prod ortamı (GitHub)

- `prod` ortamının **"Deployment branches and tags: yalnızca `v*` etiketleri"**
  kuralı kalır: prod iş akışı daldan çalıştırılamaz.
- Onay, sürüm PR'ının birleştirilmesidir. Ortamdaki **zorunlu onaylayıcı**
  kalırsa her otomatik sürüm platform sahibinin ayrıca Actions'ta onay
  tıklamasını bekler; kaldırılması önerilir (Açık sorular 1).

### Ele geçirilirse

- **"Sürüm etiketi" işi** (`contents: write`, `actions: write`, yalnızca
  `main`'e push'ta, üçüncü taraf Action yok): etiket ve dal oluşturabilir, iş
  akışı başlatabilir. `dev`/`main` kural setleri doğrudan push'u ve silmeyi,
  `v*` etiket kural seti mevcut etiketin taşınmasını ve silinmesini reddeder.
  Rastgele bir commit'e atılan etiketi prod iş akışı reddeder (birinci
  ebeveyn zinciri ve image imzası). Önceki düzende aynı etkiyi platform
  sahibinin hesabı taşıyordu; bu iş ondan daha dardır.
- **"Sürüm PR'ı kaynağı" kontrolü** atlatılırsa `main`'e `dev` dışından bir
  dal girebilir; içerik yine `main` CI'ından geçer ve imzalanır. Kontrol,
  iş PR'larının kazara `main`'e açılmasına karşıdır; güven zincirinin
  parçası değildir.
- **`dev`'e yazma** artık hiçbir ortamı etkilemez; prod'a giden yol sürüm
  PR'ından geçer. Önceki düzende `main`'e birleşen her PR doğrudan VDS'e
  gidiyordu.

## Gerekçe

- İş PR'ları ile prod yayınının ayrılması, birden çok PR'ın bir arada
  denenip tek seferde ve bilinçli olarak yayınlanmasını sağlar; platform
  sahibinin onayı sürüm başına bir kez verilir.
- Otomatik etiket, elle etiket atmanın unutulmasını ve yanlış commit'e
  atılmasını önler; imza kimliği değişmediği için cluster tarafında hiçbir
  değişiklik gerekmez.
- `workflow_dispatch` ile başlatma, yeni kimlik bilgisi eklemeden çalışan tek
  yoldur; yetkisi tek bir işte ve yalnızca `main` push'unda vardır.

## Sonuçlar

- `ci.yml`: `dev`'e push'ta da çalışır; image gönderme ve imza yalnızca
  `main`'de; `buf breaking` PR'ın hedef dalına göre (iş PR'larında `dev`,
  sürüm PR'ında `main`); yeni "Sürüm etiketi" işi.
- `prod.yml`: `workflow_dispatch` tetikleyicisi eklendi; doğrulamalar aynı.
- Yeni `surum-pr.yml`; `dependabot.yml`'de `target-branch: dev`.
- `CLAUDE.md` "İş akışı", ADR-0006 ve ADR-0010'a revizyon notu, yeni
  runbook `docs/runbooks/surum-cikarma.md`.
- Platform sahibinin GitHub ayarları (yeni `dev` dalı ve kural seti,
  `main` kural setinde merge yöntemi ve yeni zorunlu kontrol, varsayılan
  dal, `prod` ortamı) bu ADR'yi getiren PR'ın açıklamasındadır.
- `main`'e açık iş PR'ları `dev`'e çevrilir.

## Açık sorular

1. `prod` ortamındaki zorunlu onaylayıcı kaldırılsın mı? (Öneri: kaldır;
   onay sürüm PR'ında. Kalırsa her sürüm bir tıklama bekler.)
2. İleride sürümden önce dev ortamında deneme gerekirse `dev` dalından elle
   (`workflow_dispatch`) dev ortamına dağıtım eklensin mi? Bu,
   `deploy/clusters/home`'da `ci.yml@refs/heads/dev` kimliğinin de kabul
   edilmesini gerektirir.
3. `v*` etiket kural setinde **Restrict creations** da açılsın mı? Açılırsa
   "Sürüm etiketi" işinin etiket atabilmesi için GitHub Actions'ın istisna
   listesine eklenmesi gerekir; bu da ADR-0010'daki "istisna tanınmaz"
   kuralına ilk istisna olur. Öneri: kapalı kalsın; yanlış etiketi prod iş
   akışı zaten reddeder.
4. minor/major artışı için elle etiket yeterli mi, yoksa sürüm PR'ında bir
   etiketle (label) mi seçilsin?
