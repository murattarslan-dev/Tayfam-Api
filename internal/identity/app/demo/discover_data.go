package demo

import corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"

// The Keşfet demo set (issue #227, ADR-0035): six tayfas opened to Keşfet,
// each of its own demo accounts (demo-21..demo-56, apart from the closed
// set's demo-01..demo-18), with events in public places in the next weeks.
// The tayfas apply to Keşfet the normal way; the platform owner approves
// them in the panel. The owner is in none of them: Keşfet leaves out the
// tayfas the viewer is in.
//
// Around the owner's account:
//   - ownerFriends send the owner a friend request, which the owner's token
//     accepts; they go to events of Belgrad, Moda and Abant, so the owner's
//     feed shows "X and N friends".
//   - The owner blocks ownerBlocks (both in Ankara), and blocksOwner (both
//     in Bodrum) block the owner: those two tayfas do not show in the
//     owner's Keşfet (ADR-0035 §3).
//   - Kapadokya has no friend of the owner and no block: an item with no
//     friends.
//
// Every account keeps the default profile settings (attendance history
// visible to tayfa members and friends; shown to friends in Keşfet).

var discoverPeople = []person{
	{Key: "asli", Subject: "demo-21", Name: "Aslı R.", BirthDate: "1989-05-19"},
	{Key: "oguz", Subject: "demo-22", Name: "Oğuz F.", BirthDate: "1984-02-07"},
	{Key: "nazli", Subject: "demo-23", Name: "Nazlı P.", BirthDate: "1994-10-13"},
	{Key: "volkan", Subject: "demo-24", Name: "Volkan Z.", BirthDate: "1980-07-26"},
	{Key: "pinar", Subject: "demo-25", Name: "Pınar L.", BirthDate: "1992-12-01"},
	{Key: "umut", Subject: "demo-26", Name: "Umut C.", BirthDate: "1998-03-15"},
	{Key: "sinan", Subject: "demo-27", Name: "Sinan K.", BirthDate: "1986-09-22"},
	{Key: "ipek", Subject: "demo-28", Name: "İpek O.", BirthDate: "1996-06-04"},
	{Key: "hakan", Subject: "demo-29", Name: "Hakan B.", BirthDate: "1983-11-30"},
	{Key: "defne", Subject: "demo-30", Name: "Defne T.", BirthDate: "2000-01-18"},
	{Key: "erdem", Subject: "demo-31", Name: "Erdem A.", BirthDate: "1991-08-08"},
	{Key: "melis", Subject: "demo-32", Name: "Melis G.", BirthDate: "1995-04-27"},
	{Key: "tuncay", Subject: "demo-33", Name: "Tuncay E.", BirthDate: "1978-10-05"},
	{Key: "sevgi", Subject: "demo-34", Name: "Sevgi Y.", BirthDate: "1987-01-23"},
	{Key: "arda", Subject: "demo-35", Name: "Arda N.", BirthDate: "1997-07-11"},
	{Key: "eda", Subject: "demo-36", Name: "Eda S.", BirthDate: "1993-03-29"},
	{Key: "cenk", Subject: "demo-37", Name: "Cenk D.", BirthDate: "1985-12-17"},
	{Key: "gul", Subject: "demo-38", Name: "Gül H.", BirthDate: "1990-06-21"},
	{Key: "levent", Subject: "demo-39", Name: "Levent U.", BirthDate: "1976-04-02"},
	{Key: "buse", Subject: "demo-40", Name: "Buse M.", BirthDate: "1999-09-09"},
	{Key: "orhan", Subject: "demo-41", Name: "Orhan İ.", BirthDate: "1982-05-14"},
	{Key: "yasemin", Subject: "demo-42", Name: "Yasemin Ç.", BirthDate: "1988-11-03"},
	{Key: "koray", Subject: "demo-43", Name: "Koray V.", BirthDate: "1994-02-25"},
	{Key: "nur", Subject: "demo-44", Name: "Nur Ö.", BirthDate: "1991-10-30"},
	{Key: "riza", Subject: "demo-45", Name: "Rıza Ş.", BirthDate: "1975-08-19"},
	{Key: "esra", Subject: "demo-46", Name: "Esra K.", BirthDate: "1992-03-06"},
	{Key: "taner", Subject: "demo-47", Name: "Taner G.", BirthDate: "1984-12-12"},
	{Key: "dilek", Subject: "demo-48", Name: "Dilek A.", BirthDate: "1989-07-28"},
	{Key: "batu", Subject: "demo-49", Name: "Batu E.", BirthDate: "2001-05-05"},
	{Key: "sena", Subject: "demo-50", Name: "Sena T.", BirthDate: "1997-11-16"},
	{Key: "ozan", Subject: "demo-51", Name: "Ozan P.", BirthDate: "1983-06-10"},
	{Key: "ebru", Subject: "demo-52", Name: "Ebru L.", BirthDate: "1990-09-01"},
	{Key: "cihan", Subject: "demo-53", Name: "Cihan R.", BirthDate: "1979-02-14"},
	{Key: "irem", Subject: "demo-54", Name: "İrem B.", BirthDate: "1996-12-24"},
	{Key: "kerem", Subject: "demo-55", Name: "Kerem D.", BirthDate: "1988-04-16"},
	{Key: "aylin", Subject: "demo-56", Name: "Aylin F.", BirthDate: "1993-08-31"},
}

// ownerFriends send the platform owner a friend request; the owner's token
// accepts it.
var ownerFriends = []string{"nazli", "volkan", "hakan", "arda"}

// ownerBlocks are blocked by the platform owner; blocksOwner block the
// owner.
var (
	ownerBlocks = []string{"orhan", "yasemin"}
	blocksOwner = []string{"taner", "dilek"}
)

const demoNote = "Bu tayfa tanıtım amaçlı örnek (demo) içerik barındırır."

var discoverTayfas = []tayfaSpec{
	{
		Key:  "k-belgrad",
		Name: "Belgrad Ormanı Yürüyüş Demo",
		Description: "Hafta sonu sabahları Belgrad Ormanı'nda tempolu yürüyüşler; yeni gelenlere kısa rota. " +
			demoNote,
		Interests: []string{"nature"},
		Founder:   "asli",
		Members: []memberSpec{
			{Person: "oguz", Role: moderator}, {Person: "nazli", Role: member}, {Person: "volkan", Role: member},
			{Person: "pinar", Role: member}, {Person: "umut", Role: member},
		},
		Discover: true,
		Events: []eventSpec{
			{
				Key: "bentler", Type: corev1.EventType_EVENT_TYPE_HIKE, Discoverable: true,
				Title:       "Bentler arası sabah yürüyüşü",
				Description: "Neşet Suyu girişinde buluşup bentler arasında 9 km'lik orta tempolu tur.",
				Start:       at{Days: 4, Hour: 8, Minute: 30}, Hours: 4,
				PlaceName: "Neşet Suyu girişi", Lat: 41.1767, Lng: 28.9861,
				City: "İstanbul", District: "Sarıyer",
				Hike: &hikeSpec{Difficulty: 2, DistanceKm: 9, ElevationM: 180, ReturnAfter: 4},
				RSVPs: []rsvpSpec{
					{Person: "asli", Status: going}, {Person: "nazli", Status: going}, {Person: "volkan", Status: going},
					{Person: "oguz", Status: going}, {Person: "pinar", Status: maybe},
				},
			},
			{
				Key: "uzun", Type: corev1.EventType_EVENT_TYPE_HIKE, Discoverable: true,
				Title:       "Kirazlıbent uzun rota",
				Description: "Deneyimliler için 15 km'lik rota; su ve hafif atıştırmalık getirin.",
				Start:       at{Days: 18, Hour: 8}, Hours: 6,
				PlaceName: "Bahçeköy girişi", Lat: 41.1795, Lng: 28.9885,
				City: "İstanbul", District: "Sarıyer",
				Hike: &hikeSpec{Difficulty: 3, DistanceKm: 15, ElevationM: 320, ReturnAfter: 6},
				RSVPs: []rsvpSpec{
					{Person: "asli", Status: going}, {Person: "volkan", Status: going}, {Person: "umut", Status: going},
				},
			},
		},
		Notices: []noticeSpec{
			{Key: "su", Title: "Yürüyüşlerde su", Body: "Her yürüyüşe en az bir litre su getirin; parkta çeşme az."},
		},
	},
	{
		Key:  "k-moda",
		Name: "Moda Sahil Voleybol Demo",
		Description: "Moda sahilinde hafta içi akşam ve pazar sabahı plaj voleybolu. Seviye fark etmez. " +
			demoNote,
		Interests: []string{"sports"},
		Founder:   "sinan",
		Members: []memberSpec{
			{Person: "ipek", Role: moderator}, {Person: "hakan", Role: member}, {Person: "defne", Role: member},
			{Person: "erdem", Role: member}, {Person: "melis", Role: member},
		},
		Discover: true,
		Events: []eventSpec{
			{
				Key: "pazar", Type: corev1.EventType_EVENT_TYPE_SPORTS, Discoverable: true,
				Title:       "Pazar sabahı voleybol",
				Description: "İki takım kurup üç set oynuyoruz; file ve top bizden.",
				Start:       at{Days: 6, Hour: 10}, Hours: 3,
				PlaceName: "Moda sahili çay bahçesi önü", Lat: 40.9817, Lng: 29.0259,
				City: "İstanbul", District: "Kadıköy",
				Fields: map[string]string{"sport": `"volleyball"`, "venue_name": `"Moda sahili"`},
				RSVPs: []rsvpSpec{
					{Person: "sinan", Status: going}, {Person: "hakan", Status: going}, {Person: "ipek", Status: going},
					{Person: "defne", Status: going}, {Person: "erdem", Status: notGoing},
				},
			},
		},
		Notices: []noticeSpec{
			{Key: "top", Title: "Top ve file", Body: "Top ve file her maçta hazır; ayakkabısız oynanıyor, havlu getirin."},
		},
	},
	{
		Key:  "k-abant",
		Name: "Abant Gölü Kamp Demo",
		Description: "Abant Gölü çevresinde aile dostu, sakin kamplar. Kamp alanının kurallarına uyulur. " +
			demoNote,
		Interests: []string{"nature"},
		Founder:   "tuncay",
		Members: []memberSpec{
			{Person: "sevgi", Role: moderator}, {Person: "arda", Role: member}, {Person: "eda", Role: member},
			{Person: "cenk", Role: member}, {Person: "gul", Role: member},
		},
		Discover: true,
		Events: []eventSpec{
			{
				Key: "kamp", Type: corev1.EventType_EVENT_TYPE_CAMP, Discoverable: true,
				Title:       "Abant'ta hafta sonu kampı",
				Description: "Cumartesi öğlen kurulum, akşam göl kenarında yürüyüş, pazar kahvaltı sonrası toplanma.",
				Start:       at{Days: 11, Hour: 12}, Hours: 26,
				PlaceName: "Abant Gölü Milli Parkı kamp alanı", Lat: 40.6058, Lng: 31.2813,
				City: "Bolu", District: "Merkez",
				Fields: map[string]string{
					"site_name": `"Abant kamp alanı"`, "has_water": `true`, "has_toilet": `true`,
					"notes": `"Gece hava soğuk; kalın uyku tulumu getirin."`,
				},
				RSVPs: []rsvpSpec{
					{Person: "tuncay", Status: going}, {Person: "arda", Status: going}, {Person: "sevgi", Status: going},
					{Person: "eda", Status: maybe}, {Person: "cenk", Status: going},
				},
			},
		},
	},
	{
		Key:  "k-ankara",
		Name: "Ankara Fotoğraf Yürüyüşü Demo",
		Description: "Ankara'nın tarihi sokaklarında fotoğraf yürüyüşleri ve sonrasında kısa bir değerlendirme. " +
			demoNote,
		Interests: []string{"general"},
		Founder:   "levent",
		Members: []memberSpec{
			{Person: "buse", Role: moderator}, {Person: "orhan", Role: member}, {Person: "yasemin", Role: member},
			{Person: "koray", Role: member}, {Person: "nur", Role: member},
		},
		Discover: true,
		Events: []eventSpec{
			{
				Key: "kale", Type: corev1.EventType_EVENT_TYPE_GENERAL, Discoverable: true,
				Title:       "Ankara Kalesi fotoğraf yürüyüşü",
				Description: "Kale kapısından başlayıp Hamamönü'ne inen iki saatlik yürüyüş.",
				Start:       at{Days: 8, Hour: 15}, Hours: 3,
				PlaceName: "Ankara Kalesi ana kapısı", Lat: 39.9410, Lng: 32.8644,
				City: "Ankara", District: "Altındağ",
				RSVPs: []rsvpSpec{
					{Person: "levent", Status: going}, {Person: "orhan", Status: going}, {Person: "yasemin", Status: going},
					{Person: "buse", Status: going},
				},
			},
		},
	},
	{
		Key:  "k-bodrum",
		Name: "Bodrum Kıyı Balıkçıları Demo",
		Description: "Bodrum kıyılarında akşam olta balıkçılığı. Kurallara ve av yasaklarına uyulur. " +
			demoNote,
		Interests: []string{"water"},
		Founder:   "riza",
		Members: []memberSpec{
			{Person: "esra", Role: moderator}, {Person: "taner", Role: member}, {Person: "dilek", Role: member},
			{Person: "batu", Role: member}, {Person: "sena", Role: member},
		},
		Discover: true,
		Events: []eventSpec{
			{
				Key: "iskele", Type: corev1.EventType_EVENT_TYPE_FISHING, Discoverable: true,
				Title:       "Gümbet iskelesinde akşam oltası",
				Description: "Gün batımından sonra iskeleden olta; yem ortak alınır.",
				Start:       at{Days: 9, Hour: 18}, Hours: 4,
				PlaceName: "Gümbet iskelesi", Lat: 37.0326, Lng: 27.4110,
				City: "Muğla", District: "Bodrum",
				RSVPs: []rsvpSpec{
					{Person: "riza", Status: going}, {Person: "taner", Status: going}, {Person: "dilek", Status: going},
					{Person: "batu", Status: going}, {Person: "sena", Status: maybe},
				},
			},
		},
		Notices: []noticeSpec{
			{Key: "yasak", Title: "Av yasakları", Body: "Etkinlik öncesi güncel av yasaklarını ve boy sınırlarını kontrol edin."},
		},
	},
	{
		Key:  "k-kapadokya",
		Name: "Kapadokya Klasik Konvoy Demo",
		Description: "Kapadokya vadilerinde klasik otomobillerle sakin tempolu konvoy turları. " +
			demoNote,
		Interests: []string{"automotive"},
		Founder:   "ozan",
		Members: []memberSpec{
			{Person: "ebru", Role: moderator}, {Person: "cihan", Role: member}, {Person: "irem", Role: member},
			{Person: "kerem", Role: member}, {Person: "aylin", Role: member},
		},
		Discover: true,
		Events: []eventSpec{
			{
				Key: "vadi", Type: corev1.EventType_EVENT_TYPE_CONVOY, Discoverable: true,
				Title:       "Vadiler arası konvoy",
				Description: "Göreme'den Uçhisar ve Ortahisar'a uzanan, fotoğraf molalı yarım günlük tur.",
				Start:       at{Days: 14, Hour: 9}, Hours: 6,
				PlaceName: "Göreme otogarı önü", Lat: 38.6431, Lng: 34.8289,
				City: "Nevşehir", District: "Merkez",
				Route: []*corev1.LatLng{
					{Lat: 38.6431, Lng: 34.8289}, {Lat: 38.6310, Lng: 34.8046}, {Lat: 38.6250, Lng: 34.8640},
				},
				RSVPs: []rsvpSpec{
					{Person: "ozan", Status: going}, {Person: "ebru", Status: going}, {Person: "cihan", Status: going},
					{Person: "kerem", Status: maybe},
				},
				Vehicles: []vehicleSpec{
					{Person: "ozan", Make: "Volkswagen", Model: "Transporter T2", Year: 1976, Color: "Turuncu"},
					{Person: "ebru", Make: "Citroën", Model: "2CV", Year: 1981, Color: "Sarı"},
					{Person: "cihan", Make: "Tofaş", Model: "Murat 124", Year: 1977, Color: "Beyaz"},
				},
			},
		},
	},
}

// allPeople is every demo account, the closed set's and Keşfet's.
func allPeople() []person {
	return append(append([]person{}, people...), discoverPeople...)
}
