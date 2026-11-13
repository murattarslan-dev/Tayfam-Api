package demo

import corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"

// The demo data set (issue #179). Names are ordinary first names with a
// surname initial; they imitate no real person. Every text is in line with
// the community rules. Nothing here is personal data: the accounts belong
// to nobody.
//
// Keys ("elif", "ege", "ege/konvoy") name records inside this file and make
// the client_request_id of every create (requestID), so changing a key
// creates a new record on the next run. Texts may change freely: a record
// that already exists is not rewritten.

// person is a demo account.
type person struct {
	Key       string
	Subject   string // auth_identities.subject, demo-NN; e-mail is Subject@EmailDomain
	Name      string // display name, 1–40 characters
	BirthDate string // YYYY-MM-DD; every account is an adult
}

// tayfaSpec is one demo tayfa and its content.
type tayfaSpec struct {
	Key         string
	Name        string // contains "Demo" (ADR-0023)
	Description string
	Interests   []corev1.EventType
	Founder     string         // person key; creates the tayfa (admin)
	Members     []memberSpec   // joined by code, approved by the founder
	OwnerRole   corev1.Role    // the platform owner's role, added last
	Events      []eventSpec    // created by the founder
	Notices     []noticeSpec   // announcements, by the founder
	Questions   []questionSpec // Q&A
	Polls       []pollSpec     // polls
	Channels    []channelSpec  // chat channels, created by the founder
}

type memberSpec struct {
	Person string
	Role   corev1.Role // MEMBER or MODERATOR
}

// at is a time relative to the run: Days after today, at Hour:Minute
// Turkey time (UTC+3, no daylight saving).
type at struct {
	Days, Hour, Minute int
}

type eventSpec struct {
	Key         string
	Type        corev1.EventType
	Online      bool
	Title       string
	Description string
	Start       at
	Hours       int // duration
	PlaceName   string
	Lat, Lng    float64
	City        string
	District    string
	OnlineURL   string
	Capacity    int32
	Convoy      *corev1.ConvoyDetails
	Camp        *corev1.CampDetails
	Hike        *hikeSpec
	Fishing     *corev1.FishingDetails
	Gaming      *corev1.GamingDetails
	Sports      *corev1.SportsDetails
	RSVPs       []rsvpSpec
	Vehicles    []vehicleSpec  // convoy: members who answered GOING
	CampItems   []campItemSpec // camp: added by the founder
}

type hikeSpec struct {
	Difficulty  int32
	DistanceKm  float64
	ElevationM  int32
	ReturnAfter int // hours after the start
}

type rsvpSpec struct {
	Person string
	Status corev1.RsvpStatus
}

// vehicleSpec has no plate: a plate is personal data (issue #179).
type vehicleSpec struct {
	Person string
	Make   string
	Model  string
	Year   int32
	Color  string
}

type campItemSpec struct {
	Name     string
	Quantity int32
	Assigned string // person key, optional
}

type noticeSpec struct {
	Key    string
	Title  string
	Body   string
	Pinned bool
}

type questionSpec struct {
	Key     string
	Author  string
	Title   string // 10–150 characters
	Body    string
	Tags    []string
	Answers []answerSpec
}

type answerSpec struct {
	Key    string
	Author string
	Body   string
	Voters []string // helpful votes
}

type pollSpec struct {
	Key       string
	Author    string
	Question  string
	Options   []string
	Anonymous bool
	Multiple  bool
	Votes     []voteSpec
}

type voteSpec struct {
	Person  string
	Options []int // indexes into Options
}

type channelSpec struct {
	Name     string
	Staff    bool
	Messages []messageSpec
}

type messageSpec struct {
	Key    string
	Author string
	Body   string
}

// EmailDomain marks demo accounts (ADR-0023).
const EmailDomain = "demo.tayfam.app"

var people = []person{
	{Key: "elif", Subject: "demo-01", Name: "Elif K.", BirthDate: "1986-04-12"},
	{Key: "burak", Subject: "demo-02", Name: "Burak T.", BirthDate: "1983-09-03"},
	{Key: "zeynep", Subject: "demo-03", Name: "Zeynep A.", BirthDate: "1992-01-27"},
	{Key: "emre", Subject: "demo-04", Name: "Emre Y.", BirthDate: "1990-06-18"},
	{Key: "selin", Subject: "demo-05", Name: "Selin D.", BirthDate: "1995-11-02"},
	{Key: "can", Subject: "demo-06", Name: "Can Ö.", BirthDate: "1979-03-21"},
	{Key: "deniz", Subject: "demo-07", Name: "Deniz Ç.", BirthDate: "1988-08-09"},
	{Key: "mert", Subject: "demo-08", Name: "Mert S.", BirthDate: "1993-05-14"},
	{Key: "ece", Subject: "demo-09", Name: "Ece B.", BirthDate: "1997-02-23"},
	{Key: "kaan", Subject: "demo-10", Name: "Kaan E.", BirthDate: "1985-12-30"},
	{Key: "gizem", Subject: "demo-11", Name: "Gizem Ş.", BirthDate: "1991-07-07"},
	{Key: "onur", Subject: "demo-12", Name: "Onur G.", BirthDate: "1981-10-16"},
	{Key: "damla", Subject: "demo-13", Name: "Damla U.", BirthDate: "1996-04-04"},
	{Key: "baris", Subject: "demo-14", Name: "Barış H.", BirthDate: "1977-01-11"},
	{Key: "ceren", Subject: "demo-15", Name: "Ceren İ.", BirthDate: "1994-09-25"},
	{Key: "tolga", Subject: "demo-16", Name: "Tolga M.", BirthDate: "1987-03-08"},
	{Key: "merve", Subject: "demo-17", Name: "Merve N.", BirthDate: "1999-06-29"},
	{Key: "serkan", Subject: "demo-18", Name: "Serkan V.", BirthDate: "1982-02-15"},
}

const (
	member    = corev1.Role_ROLE_MEMBER
	moderator = corev1.Role_ROLE_MODERATOR
	admin     = corev1.Role_ROLE_ADMIN

	going    = corev1.RsvpStatus_RSVP_STATUS_GOING
	maybe    = corev1.RsvpStatus_RSVP_STATUS_MAYBE
	notGoing = corev1.RsvpStatus_RSVP_STATUS_NOT_GOING
)

var tayfas = []tayfaSpec{
	{
		Key:  "ege",
		Name: "Ege Klasikleri Demo",
		Description: "Klasik otomobil tutkunlarının Ege turları, bakım sohbetleri ve buluşmaları. " +
			"Bu tayfa tanıtım amaçlı örnek (demo) içerik barındırır.",
		Interests: []corev1.EventType{corev1.EventType_EVENT_TYPE_CONVOY, corev1.EventType_EVENT_TYPE_GENERAL},
		Founder:   "elif",
		Members: []memberSpec{
			{Person: "can", Role: moderator}, {Person: "kaan", Role: member}, {Person: "onur", Role: member},
			{Person: "baris", Role: member}, {Person: "serkan", Role: member}, {Person: "tolga", Role: member},
			{Person: "selin", Role: member},
		},
		OwnerRole: admin,
		Events: []eventSpec{
			{
				Key: "konvoy", Type: corev1.EventType_EVENT_TYPE_CONVOY,
				Title:       "Bozdağ sonbahar konvoyu",
				Description: "Kemalpaşa'dan Bozdağ'a sakin tempoda bir tur. Öğle yemeği yaylada, dönüş gün batımından önce.",
				Start:       at{Days: 9, Hour: 8, Minute: 30}, Hours: 9,
				PlaceName: "Kemalpaşa çıkışı dinlenme tesisi", Lat: 38.4262, Lng: 27.4171,
				City: "İzmir", District: "Kemalpaşa",
				Convoy: &corev1.ConvoyDetails{Route: []*corev1.LatLng{
					{Lat: 38.4262, Lng: 27.4171}, {Lat: 38.3369, Lng: 27.8915}, {Lat: 38.3427, Lng: 28.1079},
				}},
				RSVPs: []rsvpSpec{
					{Person: "elif", Status: going}, {Person: "can", Status: going}, {Person: "kaan", Status: going},
					{Person: "onur", Status: going}, {Person: "baris", Status: maybe}, {Person: "serkan", Status: going},
					{Person: "tolga", Status: notGoing},
				},
				Vehicles: []vehicleSpec{
					{Person: "elif", Make: "Mercedes-Benz", Model: "280 SE", Year: 1972, Color: "Krem"},
					{Person: "can", Make: "Volkswagen", Model: "Kaplumbağa", Year: 1968, Color: "Açık mavi"},
					{Person: "kaan", Make: "Ford", Model: "Mustang", Year: 1967, Color: "Kırmızı"},
					{Person: "onur", Make: "Fiat", Model: "124", Year: 1974, Color: "Beyaz"},
					{Person: "serkan", Make: "Renault", Model: "12", Year: 1979, Color: "Bej"},
				},
			},
			{
				Key: "restorasyon", Type: corev1.EventType_EVENT_TYPE_GENERAL, Online: true,
				Title:       "Restorasyon sohbeti (çevrim içi)",
				Description: "Kış bakımı öncesi parça tedariki ve usta önerileri üzerine bir saatlik sohbet.",
				Start:       at{Days: 5, Hour: 21}, Hours: 1,
				OnlineURL: "https://example.com/tayfam-demo/restorasyon",
				RSVPs: []rsvpSpec{
					{Person: "elif", Status: going}, {Person: "selin", Status: going},
					{Person: "baris", Status: going}, {Person: "kaan", Status: maybe},
				},
			},
		},
		Notices: []noticeSpec{
			{
				Key: "kurallar", Pinned: true,
				Title: "Konvoy kuralları",
				Body: "Konvoyda sıra bozulmaz, öndeki aracı gözden kaybetmeyin.\n" +
					"Mola yerleri etkinlik açıklamasında; telsiz kanalı buluşmada paylaşılır.\n" +
					"Hız sınırlarına uyuyoruz, kimse yarışmıyor.",
			},
			{
				Key:   "hosgeldin",
				Title: "Yeni üyelere hoş geldiniz",
				Body:  "Aramıza yeni katılanlara hoş geldiniz! Aracınızı ve hikâyesini genel kanalda paylaşmayı unutmayın.",
			},
		},
		Questions: []questionSpec{
			{
				Key: "karburator", Author: "kaan",
				Title: "Çift gövdeli karbüratör ayarını kim yapıyor?",
				Body:  "Rölantide tekleme var, şamandıra seviyesi doğru görünüyor. Bornova civarında önerebileceğiniz bir usta var mı?",
				Tags:  []string{"bakım", "karbüratör"},
				Answers: []answerSpec{
					{Key: "1", Author: "can", Body: "Önce vakum kaçağına bak; hortumlar eskiyse rölanti hep oynar. Sonra senkron ayarı yapılır.", Voters: []string{"kaan", "onur", "elif"}},
					{Key: "2", Author: "onur", Body: "Geçen ay benimkini Işıkkent'teki sanayide yaptırdım, memnun kaldım. Adresi özelden yazarım.", Voters: []string{"kaan"}},
				},
			},
		},
		Polls: []pollSpec{
			{
				Key: "rota", Author: "elif",
				Question: "Bir sonraki konvoy rotası nereye olsun?",
				Options:  []string{"Şirince", "Alaçatı", "Bozdağ", "Kozak Yaylası"},
				Votes: []voteSpec{
					{Person: "can", Options: []int{0}}, {Person: "kaan", Options: []int{3}}, {Person: "onur", Options: []int{0}},
					{Person: "baris", Options: []int{1}}, {Person: "serkan", Options: []int{3}}, {Person: "selin", Options: []int{0}},
				},
			},
			{
				Key: "aidat", Author: "can", Anonymous: true,
				Question: "Yıllık kulüp aidatı ne kadar olsun?",
				Options:  []string{"Aidat olmasın", "500 TL", "1000 TL"},
				Votes: []voteSpec{
					{Person: "elif", Options: []int{1}}, {Person: "kaan", Options: []int{1}}, {Person: "onur", Options: []int{0}},
					{Person: "baris", Options: []int{2}}, {Person: "tolga", Options: []int{1}}, {Person: "serkan", Options: []int{1}},
				},
			},
		},
		Channels: []channelSpec{
			{Name: "genel", Messages: []messageSpec{
				{Key: "1", Author: "elif", Body: "Günaydın! Bozdağ konvoyu için katılımları etkinlik sayfasından işaretlemeyi unutmayın."},
				{Key: "2", Author: "kaan", Body: "Mustang'in fren balatalarını yeniledim, bu sefer yokuşlarda sorun yok 😊"},
				{Key: "3", Author: "can", Body: "Kaplumbağa hazır. Yaylada çay molası şart."},
				{Key: "4", Author: "serkan", Body: "Bej Renault 12 de geliyor, arkadan takip ederim."},
				{Key: "5", Author: "onur", Body: "Hava durumuna baktım, cumartesi güneşli görünüyor."},
			}},
			{Name: "yönetim", Staff: true, Messages: []messageSpec{
				{Key: "1", Author: "elif", Body: "Konvoyda öncü ve kapanış araçlarını belirleyelim mi?"},
				{Key: "2", Author: "can", Body: "Öncü ben olayım, kapanışa Serkan uygun olur."},
			}},
		},
	},
	{
		Key:  "kazdagi",
		Name: "Kazdağı Kampçıları Demo",
		Description: "Kazdağları eteklerinde kamp, doğa ve kıyıda akşam balığı. " +
			"Bu tayfa tanıtım amaçlı örnek (demo) içerik barındırır.",
		Interests: []corev1.EventType{corev1.EventType_EVENT_TYPE_CAMP, corev1.EventType_EVENT_TYPE_FISHING},
		Founder:   "burak",
		Members: []memberSpec{
			{Person: "deniz", Role: moderator}, {Person: "mert", Role: member}, {Person: "ece", Role: member},
			{Person: "gizem", Role: member}, {Person: "damla", Role: member}, {Person: "tolga", Role: member},
			{Person: "kaan", Role: member},
		},
		OwnerRole: moderator,
		Events: []eventSpec{
			{
				Key: "kamp", Type: corev1.EventType_EVENT_TYPE_CAMP,
				Title:       "Kazdağı hafta sonu kampı",
				Description: "Cuma akşamı kurulum, cumartesi kısa yürüyüş ve şelale, pazar öğlen toplanma.",
				Start:       at{Days: 12, Hour: 18}, Hours: 42,
				PlaceName: "Milli park girişine yakın kamp alanı", Lat: 39.7031, Lng: 26.8542,
				City: "Balıkesir", District: "Edremit",
				Camp: &corev1.CampDetails{
					SiteName: "Kazdağı kamp alanı", HasWater: ptr(true), HasToilet: ptr(true),
					Notes: "Geceleri hava serin, uyku tulumunuzu kalın seçin. Ateş yalnızca ocak alanında yakılır.",
				},
				RSVPs: []rsvpSpec{
					{Person: "burak", Status: going}, {Person: "deniz", Status: going}, {Person: "mert", Status: going},
					{Person: "ece", Status: going}, {Person: "gizem", Status: maybe}, {Person: "damla", Status: going},
					{Person: "tolga", Status: notGoing},
				},
				CampItems: []campItemSpec{
					{Name: "Çadır (4 kişilik)", Quantity: 2, Assigned: "burak"},
					{Name: "Kamp ocağı ve tüp", Quantity: 1, Assigned: "deniz"},
					{Name: "Su bidonu (19 L)", Quantity: 3, Assigned: "mert"},
					{Name: "İlk yardım çantası", Quantity: 1, Assigned: "ece"},
					{Name: "Kahvaltılık", Quantity: 1},
				},
			},
			{
				Key: "balik", Type: corev1.EventType_EVENT_TYPE_FISHING,
				Title:       "Akçay sahilinde akşam balığı",
				Description: "Gün batımına doğru kıyıdan olta. Yem ortak alınacak, isteyen yedek takım getirsin.",
				Start:       at{Days: 6, Hour: 17, Minute: 30}, Hours: 4,
				PlaceName: "Akçay sahil yolu", Lat: 39.5838, Lng: 26.9265,
				City: "Balıkesir", District: "Edremit",
				Fishing: &corev1.FishingDetails{SpotVisibility: corev1.SpotVisibility_SPOT_VISIBILITY_PARTICIPANTS},
				RSVPs: []rsvpSpec{
					{Person: "burak", Status: going}, {Person: "mert", Status: going},
					{Person: "kaan", Status: going}, {Person: "gizem", Status: maybe},
				},
			},
		},
		Notices: []noticeSpec{
			{
				Key: "iz", Pinned: true,
				Title: "İz bırakmama ilkesi",
				Body:  "Getirdiğimiz her şeyi geri götürüyoruz. Çöp poşetleri ortak malzeme listesinde.",
			},
		},
		Questions: []questionSpec{
			{
				Key: "tulum", Author: "ece",
				Title: "Ekim sonunda hangi uyku tulumu yeterli olur?",
				Body:  "İlk kez bu mevsimde kamp yapacağım. Konfor sıcaklığı kaç derece olan bir tulum almalıyım?",
				Tags:  []string{"ekipman"},
				Answers: []answerSpec{
					{Key: "1", Author: "deniz", Body: "Konfor sıcaklığı 0–5 °C olan bir tulum ve altına iyi bir mat yeterli olur. Mat en az tulum kadar önemli.", Voters: []string{"ece", "gizem", "burak"}},
					{Key: "2", Author: "mert", Body: "Yanına ince bir polar astar al, gece soğursa işini görür.", Voters: []string{"ece"}},
				},
			},
		},
		Polls: []pollSpec{
			{
				Key: "yemek", Author: "burak", Multiple: true,
				Question: "Kampta akşam ne pişirelim?",
				Options:  []string{"Sac kavurma", "Menemen", "Közde sebze", "Mantar sote"},
				Votes: []voteSpec{
					{Person: "deniz", Options: []int{0, 2}}, {Person: "mert", Options: []int{0}}, {Person: "ece", Options: []int{2, 3}},
					{Person: "damla", Options: []int{1, 2}}, {Person: "gizem", Options: []int{2}},
				},
			},
			{
				Key: "tarih", Author: "deniz", Anonymous: true,
				Question: "Bahar kampı için hangi ay daha uygun?",
				Options:  []string{"Nisan", "Mayıs", "Haziran"},
				Votes: []voteSpec{
					{Person: "burak", Options: []int{1}}, {Person: "mert", Options: []int{1}}, {Person: "ece", Options: []int{0}},
					{Person: "damla", Options: []int{1}}, {Person: "tolga", Options: []int{2}},
				},
			},
		},
		Channels: []channelSpec{
			{Name: "genel", Messages: []messageSpec{
				{Key: "1", Author: "burak", Body: "Malzeme listesini etkinliğe ekledim, üstüne yazılmayanları paylaşalım."},
				{Key: "2", Author: "ece", Body: "İlk yardım çantası bende, içine yara bandı ve böcek kovucu da koydum."},
				{Key: "3", Author: "deniz", Body: "Ocak ve tüp bende. Yedek kartuş da getiriyorum."},
				{Key: "4", Author: "damla", Body: "Şelale yürüyüşü için fotoğraf makinemi getireceğim 📷"},
			}},
			{Name: "yönetim", Staff: true, Messages: []messageSpec{
				{Key: "1", Author: "burak", Body: "Kamp alanı rezervasyonunu cuma sabahı teyit ediyorum."},
			}},
		},
	},
	{
		Key:  "nif",
		Name: "Nif Yürüyüş Demo",
		Description: "İzmir çevresinde hafta sonu doğa yürüyüşleri; her seviyeye uygun rotalar. " +
			"Bu tayfa tanıtım amaçlı örnek (demo) içerik barındırır.",
		Interests: []corev1.EventType{corev1.EventType_EVENT_TYPE_HIKE, corev1.EventType_EVENT_TYPE_GENERAL},
		Founder:   "zeynep",
		Members: []memberSpec{
			{Person: "selin", Role: moderator}, {Person: "ceren", Role: member}, {Person: "merve", Role: member},
			{Person: "deniz", Role: member}, {Person: "can", Role: member}, {Person: "gizem", Role: member},
			{Person: "onur", Role: member},
		},
		OwnerRole: member,
		Events: []eventSpec{
			{
				Key: "zirve", Type: corev1.EventType_EVENT_TYPE_HIKE,
				Title:       "Nif Dağı zirve yürüyüşü",
				Description: "Orta zorlukta, çam ormanı içinden zirveye. Yanınızda en az 2 litre su ve kat kat giysi olsun.",
				Start:       at{Days: 8, Hour: 8}, Hours: 9,
				PlaceName: "Kemalpaşa köy meydanı", Lat: 38.3812, Lng: 27.3719,
				City: "İzmir", District: "Kemalpaşa",
				Hike:     &hikeSpec{Difficulty: 3, DistanceKm: 14.5, ElevationM: 1100, ReturnAfter: 7},
				Capacity: 15,
				RSVPs: []rsvpSpec{
					{Person: "zeynep", Status: going}, {Person: "selin", Status: going}, {Person: "ceren", Status: going},
					{Person: "merve", Status: going}, {Person: "deniz", Status: maybe}, {Person: "can", Status: going},
					{Person: "onur", Status: notGoing},
				},
			},
			{
				Key: "kahvalti", Type: corev1.EventType_EVENT_TYPE_GENERAL,
				Title:       "Yürüyüş sonrası köy kahvaltısı",
				Description: "Rotayı planlamak ve tanışmak için kahvaltıda buluşuyoruz.",
				Start:       at{Days: 15, Hour: 10}, Hours: 2,
				PlaceName: "Köy kahvaltı evi", Lat: 38.4061, Lng: 27.3528,
				City: "İzmir", District: "Kemalpaşa",
				RSVPs: []rsvpSpec{
					{Person: "zeynep", Status: going}, {Person: "merve", Status: going},
					{Person: "gizem", Status: going}, {Person: "ceren", Status: maybe},
				},
			},
		},
		Notices: []noticeSpec{
			{
				Key: "guvenlik", Pinned: true,
				Title: "Yürüyüş güvenliği",
				Body: "Gruptan ayrılmıyoruz; en arkada her zaman bir rehber var.\n" +
					"Yola çıkarken uygulamadan \"Yola çıktım\" deyin, dönüşte \"Döndüm\" demeyi unutmayın.",
			},
			{
				Key:   "sezon",
				Title: "Kış sezonu programı",
				Body:  "Kasım–şubat arasında rotalar kısalıyor; başlangıç saatleri bir saat öne alınıyor.",
			},
		},
		Questions: []questionSpec{
			{
				Key: "bot", Author: "merve",
				Title: "Yeni başlayan için hangi yürüyüş botu uygun?",
				Body:  "Şimdiye kadar spor ayakkabıyla yürüdüm. Su geçirmez bot şart mı, yoksa patika ayakkabısı yeterli mi?",
				Tags:  []string{"ekipman", "yeni başlayan"},
				Answers: []answerSpec{
					{Key: "1", Author: "selin", Body: "Kışın taşlı ve çamurlu rotalarda bilek destekli, su geçirmez bot çok fark ediyor. Yazın patika ayakkabısı yeterli.", Voters: []string{"merve", "ceren", "zeynep"}},
					{Key: "2", Author: "ceren", Body: "Almadan önce mağazada eğimli rampada denemeni öneririm, ayak parmakları öne vurmamalı.", Voters: []string{"merve"}},
				},
			},
		},
		Polls: []pollSpec{
			{
				Key: "rota", Author: "zeynep",
				Question: "Aralık yürüyüşü hangi rotada olsun?",
				Options:  []string{"Spil Dağı", "Karagöl", "Kemalpaşa şelaleleri"},
				Votes: []voteSpec{
					{Person: "selin", Options: []int{0}}, {Person: "ceren", Options: []int{2}}, {Person: "merve", Options: []int{2}},
					{Person: "deniz", Options: []int{1}}, {Person: "can", Options: []int{2}}, {Person: "gizem", Options: []int{0}},
				},
			},
			{
				Key: "zorluk", Author: "selin", Anonymous: true,
				Question: "Son yürüyüşün zorluğu sana nasıl geldi?",
				Options:  []string{"Kolaydı", "Tam kıvamında", "Zorlandım"},
				Votes: []voteSpec{
					{Person: "zeynep", Options: []int{1}}, {Person: "ceren", Options: []int{1}}, {Person: "merve", Options: []int{2}},
					{Person: "can", Options: []int{0}}, {Person: "onur", Options: []int{1}},
				},
			},
		},
		Channels: []channelSpec{
			{Name: "genel", Messages: []messageSpec{
				{Key: "1", Author: "zeynep", Body: "Pazar sabahı 08.00'de meydandayız. Geç kalan olursa mesaj atsın."},
				{Key: "2", Author: "ceren", Body: "Batonlarımı getiriyorum, yedek isteyen olursa söylesin."},
				{Key: "3", Author: "selin", Body: "Zirvede rüzgâr olabilir, rüzgârlık almayı unutmayın."},
				{Key: "4", Author: "merve", Body: "İlk zirve yürüyüşüm, çok heyecanlıyım!"},
				{Key: "5", Author: "can", Body: "Hoş geldin Merve, tempo rahat, keyifli geçecek."},
			}},
		},
	},
	{
		Key:  "urla",
		Name: "Urla Halısaha Demo",
		Description: "Haftalık halı saha maçı ve hafta içi çevrim içi oyun akşamları. " +
			"Bu tayfa tanıtım amaçlı örnek (demo) içerik barındırır.",
		Interests: []corev1.EventType{corev1.EventType_EVENT_TYPE_SPORTS, corev1.EventType_EVENT_TYPE_GAMING},
		Founder:   "emre",
		Members: []memberSpec{
			{Person: "mert", Role: moderator}, {Person: "kaan", Role: member}, {Person: "baris", Role: member},
			{Person: "tolga", Role: member}, {Person: "serkan", Role: member}, {Person: "onur", Role: member},
			{Person: "damla", Role: member}, {Person: "ceren", Role: member},
		},
		OwnerRole: member,
		Events: []eventSpec{
			{
				Key: "mac", Type: corev1.EventType_EVENT_TYPE_SPORTS,
				Title:       "Perşembe akşamı halı saha",
				Description: "7'ye 7 maç. Formalar iki renk, açık ve koyu tişört getirin. Saha ücreti maç sonunda paylaşılır.",
				Start:       at{Days: 4, Hour: 20}, Hours: 1,
				PlaceName: "Urla halı saha", Lat: 38.3227, Lng: 26.7647,
				City: "İzmir", District: "Urla",
				Capacity: 14,
				Sports:   &corev1.SportsDetails{Sport: corev1.Sport_SPORT_FOOTBALL, VenueName: "Urla halı saha"},
				RSVPs: []rsvpSpec{
					{Person: "emre", Status: going}, {Person: "mert", Status: going}, {Person: "kaan", Status: going},
					{Person: "baris", Status: going}, {Person: "tolga", Status: going}, {Person: "serkan", Status: maybe},
					{Person: "onur", Status: going}, {Person: "damla", Status: notGoing},
				},
			},
			{
				Key: "turnuva", Type: corev1.EventType_EVENT_TYPE_GAMING, Online: true,
				Title:       "Çevrim içi futbol turnuvası",
				Description: "Eleme usulü, maç başına 2 x 6 dakika. Eşleşmeler etkinlik günü kanalda paylaşılır.",
				Start:       at{Days: 10, Hour: 21}, Hours: 3,
				OnlineURL: "https://example.com/tayfam-demo/turnuva",
				Gaming:    &corev1.GamingDetails{Game: "Futbol simülasyonu", Platform: corev1.GamingPlatform_GAMING_PLATFORM_PLAYSTATION},
				RSVPs: []rsvpSpec{
					{Person: "emre", Status: going}, {Person: "kaan", Status: going}, {Person: "ceren", Status: going},
					{Person: "tolga", Status: maybe}, {Person: "onur", Status: going},
				},
			},
		},
		Notices: []noticeSpec{
			{
				Key: "saha", Pinned: true,
				Title: "Saha kuralları",
				Body:  "Maçtan 15 dakika önce sahada olalım. Katılamayacaksan en geç çarşamba akşamı katılımını güncelle.",
			},
		},
		Questions: []questionSpec{
			{
				Key: "krampon", Author: "ceren",
				Title: "Halı saha için krampon mu, halı saha ayakkabısı mı?",
				Body:  "Yeni başlıyorum, sahada uzun krampon yasak mı? Ne almam gerekiyor?",
				Tags:  []string{"ekipman"},
				Answers: []answerSpec{
					{Key: "1", Author: "mert", Body: "Uzun krampon yasak; kısa dişli halı saha ayakkabısı al. Bileğini de daha az zorlar.", Voters: []string{"ceren", "emre"}},
				},
			},
		},
		Polls: []pollSpec{
			{
				Key: "gun", Author: "emre",
				Question: "Maç günü değişsin mi?",
				Options:  []string{"Perşembe kalsın", "Salıya alalım", "Cumartesi sabahı"},
				Votes: []voteSpec{
					{Person: "mert", Options: []int{0}}, {Person: "kaan", Options: []int{0}}, {Person: "baris", Options: []int{2}},
					{Person: "tolga", Options: []int{0}}, {Person: "serkan", Options: []int{1}}, {Person: "onur", Options: []int{0}},
				},
			},
			{
				Key: "kaptan", Author: "mert", Anonymous: true,
				Question: "Turnuva formatı nasıl olsun?",
				Options:  []string{"Eleme", "Lig usulü", "Grup + eleme"},
				Votes: []voteSpec{
					{Person: "emre", Options: []int{2}}, {Person: "kaan", Options: []int{2}}, {Person: "ceren", Options: []int{0}},
					{Person: "onur", Options: []int{2}}, {Person: "tolga", Options: []int{1}},
				},
			},
		},
		Channels: []channelSpec{
			{Name: "genel", Messages: []messageSpec{
				{Key: "1", Author: "emre", Body: "Perşembe kadro 14'e yakın, katılımını işaretlemeyen kaldı mı?"},
				{Key: "2", Author: "baris", Body: "Ben varım, koyu tişörtlü takımdayım."},
				{Key: "3", Author: "kaan", Body: "Geçen haftanın rövanşını alıyoruz ⚽"},
				{Key: "4", Author: "mert", Body: "Topu ben getiriyorum, pompası da bende."},
			}},
			{Name: "turnuva", Messages: []messageSpec{
				{Key: "1", Author: "emre", Body: "Turnuva eşleşmeleri etkinlik günü burada paylaşılacak."},
				{Key: "2", Author: "ceren", Body: "Kolu şarja taktım, hazırım!"},
			}},
		},
	},
}

func ptr[T any](v T) *T { return &v }
