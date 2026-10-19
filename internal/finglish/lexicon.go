package finglish

import "strings"

// The words, as tables: the Persian first, then after «|» the Latin spellings
// it is typed as. «_» in the Persian is a half-space (ZWNJ), written visibly
// because an invisible character in a source file is one nobody can review.
//
// ONE SPELLING PER SOUND IS ENOUGH. Every Latin spelling goes through canon
// before it is stored, exactly as a typed word does, so «gheymat», «qeymat»
// and «gheimat» are one key and a line only has to list the spellings canon
// does NOT already bring together -- a different vowel, a dropped letter.
//
// Two tables, and which one a word is in is the whole of Is's judgement:
//
//	distinctive  only Finglish writes these. «sefaresh», «chie», «lotfan»
//	             are not English or German words, so one of them is evidence.
//	ambiguous    Finglish too, but also a word of English or German business
//	             mail: «in», «to», «man», «bar», «order», «proforma». They are
//	             rendered when the text IS Finglish, and are never evidence
//	             that it is -- «Zaman wants to be in the office» holds five of
//	             them and is English.
//
// A word in both tables, or two Persian words on one Latin key, stops the
// package from loading (see load): a lexicon that quietly kept whichever line
// came last would render a word differently after an innocent reordering.

const distinctive = `
سلام | salam slm
خوبی | khobi khubi
خوب | khob khub
خوبم | khobam khubam
مرسی | mersi
ممنون | mamnun mamnon
سپاس | sepas
درود | dorud
خداحافظ | khodahafez khodafez
لطفا | lotfan lotfa lotfn
نخیر | nakheyr nakhir
خیلی | kheyli khyli khili
شما | shoma
ایشان | ishan
اینجا | inja
اینها | inha
اونجا | unja
همه | hame
همین | hamin
همون | hamun
اگر | agar
ولی | vali
چون | chon
قبل | ghabl
قبلی | ghabli
بعدی | badi
برای | baraye bara
مثل | mesle
درباره | darbare
مربوط | marbut marbot
چیز | chiz
چیزی | chizi
هیچ | hich
کسی | kasi
هنوز | hanuz
فعلا | felan
زود | zud
آخر | akhar
آخرین | akharin akhrin
اخیر | akhir
جدید | jadid
قدیمی | ghadimi
اول | aval
دوم | dovom
تمام | tamam tamum
باز | baz
بسته | baste
کلی | koli
تعداد | tedad
بیشتر | bishtar
بیشترین | bishtarin
کمتر | kamtar
کمترین | kamtarin
برتر | bartar
بهتر | behtar
بهترین | behtarin
میانگین | miangin mianegin
درست | dorost
غلط | ghalat
کجا | koja
کجاست | kojast kojas
چیه | chie
کیه | kie
چرا | chera
چطور | chetor chetur
چطوری | chetori
چطوره | chetore
کدام | kodam kodom kodum
چند | chand
چندتا | chanta chandta
چقدر | cheghadr cheghad cheghadar
نیست | nist
هستند | hastand hastan
شد | shod
شده | shode
نشده | nashode
می_شه | mishe
نمی_شه | nemishe
نداره | nadare
داریم | darim
نداریم | nadarim
داشتیم | dashtim
داشتم | dashtam
داری | dari
باید | bayad
می_خوام | mikham
می_خواستم | mikhastam
می_خوای | mikhay
می_تونم | mitunam
می_تونی | mituni mitoni
کن | kon
کنید | konid
کنم | konam
کرد | kard
کردم | kardam
کرده | karde
بکن | bokon
بده | bede
بدید | bedid
بگو | begu
بگید | begid
ببین | bebin
ببینم | bebinam
بفرست | befrest befres
بفرستید | befrestid
بنویس | benevis
نشون | neshun
نشان | neshan
بیا | bia
بیار | biar
بنداز | bendaz
یادم | yadam
یاد | yad
امروز | emruz emroz
دیروز | diruz diroz
فردا | farda
پس_فردا | pasfarda
هفته | hafte
هفتگی | haftegi
ماه | mah
ماهانه | mahane
امسال | emsal
پارسال | parsal
سالانه | salane saliane
روز | ruz roz
روزانه | ruzane
زمان | zaman
تاریخ | tarikh
گذشته | gozashte
آینده | ayande
جاری | jari
صبح | sobh
شب | shab
یک | yek
یکی | yeki
چهار | chahar
پنج | panj
شش | shesh shish
هشت | hasht
یازده | yazdah
دوازده | davazdah
هزار | hezar
میلیارد | milyard miliard
سفارش | sefaresh sefarsh
سفارشات | sefareshat
پیش | pish
پیش_فاکتور | pishfactor pishfaktor
قیمت | gheymat geymat ghymat
استعلام | estelam
پیشنهاد | pishnahad pishnehad
مناقصه | monaghese monakese
مزایده | mozayede
قرارداد | gharardad gharadad
تحویل | tahvil
ارسال | ersal
حمل | haml
نقل | naghl
گمرک | gomrok gomruk
ترخیص | tarkhis
ضمانت | zemanat zamanat
ضمانت_نامه | zemanatname zamanatname
پرداخت | pardakht
پیش_پرداخت | pishpardakht
دریافت | daryaft
دریافتی | daryafti
پیش_دریافت | pishdaryaft
واریز | variz
قسط | ghest
اقساط | aghsat
مشتری | moshtari mushtari moshtri
مشتریان | moshtarian
خریدار | kharidar
فروشنده | forushande foroshande
تأمین | tamin
کننده | konande
تأمین_کننده | taminkonande
پیگیری | peygiri pigiri
جلسه | jalase jalese
وضعیت | vaziat vazeyat vazyat
گزارش | gozaresh
بررسی | baresi
علت | elat
تأخیر | takhir
عقب | aghab
افتاده | oftade
مونده | munde
مشکل | moshkel
ناراضی | narazi
ایمیل | imeyl emeyl imel
مکاتبه | mokatebe
مکاتبات | mokatebat
سند | sanad
اسناد | asnad
مدرک | madrak
مدارک | madarek
مرحله | marhale
مراحل | marahel
گردش | gardesh
تاریخچه | tarikhche
یادآوری | yadavari yadavar
خلاصه | kholase
مدیریتی | modiriati
مدیر | modir
خبر | khabar
شروع | shoru
دوباره | dobare
آماده | amade
تهیه | tahie tahye
پیش_نویس | pishnevis
پیام | payam
جواب | javab
پاسخ | pasokh
واتساپ | vatsap
لغو | lagv laghv
تأیید | taid tayid taiid
تخفیف | takhfif
تماس | tamas
همکار | hamkar
کارمند | karmand
کارشناس | karshenas
عملکرد | amalkard
نتیجه | natije
برنده | barande
ضرر | zarar
درآمد | daramad
هزینه | hazine
بدهی | bedehi
طلب | talab
مبلغ | mablagh
ارز | arz
تومان | toman tuman
فروش | forush forosh
خرید | kharid
انبار | anbar
موجودی | mojudi mojodi
محموله | mahmule mahmole
بارنامه | barname
حساب | hesab
صورت_حساب | surathesab
بیمه | bime
مالیات | maliat
سررسید | saresid
رسید | resid
تمدید | tamdid
ابطال | ebtal
معوق | moavagh
معطل | moatal
بازرگانی | bazargani
کامیون | kamion kamiun
کشتی | keshti
هوایی | havayi havaei
دریایی | daryayi daryaei
زمینی | zamini
قطعه | ghete
قطعات | ghataat ghetaat
دستگاه | dastgah
مهندس | mohandes
آقا | agha
خانم | khanom khanum
اطلاعات | etelaat
فهرست | fehrest
شماره | shomare
دست | dast
قبول | ghabul
شهر | shahr
هوا | hava
میزان | mizan
برگشتی | bargashti
مدتی | modati
نداده | nadade
نداده_اند | nadadeand nadadand nadadan
بزرگ | bozorg
بزرگ_تر | bozorgtar
بزرگ_ترین | bozorgtarin
نزدیک | nazdik
گلوگاه | gologah golgah
شکست | shekast
روند | ravand
جریان | jarian jaryan
راکد | raked
خارجی | khareji
داخلی | dakheli
چک | chek
ثبت | sabt
انتظار | entezar
های | haye
`

const ambiguous = `
این | in
اون | un
آن | an
و | va o
با | ba
بی | bi
از | az
به | be
در | dar
تا | ta
که | ke
رو | ro ru
را | ra
هم | ham
یا | ya
اگه | age
اما | amma
پس | pas
بعد | bad
واسه | vase
تو | to tu
من | man
ما | ma
نه | na
بله | bale
آره | are
هست | hast
است | ast
بود | bud
داره | dare
حال | hal
سال | sal
کل | kol
جمع | jam
چی | chi
چه | che
کی | ki key
- | e ye
دو | do
سه | se
چهار | char
هفت | haft
نه | noh
ده | dah
سی | si
صد | sad
بیست | bist
هر | har
فورواردر | forwarder
میلیون | milion milyun
الان | alan
حالا | hala
دیر | dir
مرسی | merci
بار | bar
کار | kar
کالا | kala
ساعت | saat
نامه | name
فاکتور | factor faktor
پیش_فاکتور | proforma perfoma
سفارش | order
ایمیل | email
واتساپ | whatsapp
فایل | file
مانده | mande
گیر | gir
سر | sar
شرکت | sherkat
سود | sud
دلار | dolar dollar
یورو | euro yuro
ریال | rial
بانک | bank
کد | kod
تیم | tim
تلفن | telefon
آزاد | azad
رد | rad
دکتر | doktor
نیروگاه | nirugah
پالایشگاه | palayeshgah
پتروشیمی | petroshimi
فولاد | folad fulad
صنعت | sanat
کارخانه | karkhane
تولید | tolid
ها | ha
های | hay
`

// neutral are the English words people drop into a Finglish sentence -- «hi»,
// «ok», «lotfan status ro check kon». They are left exactly as typed, and they
// count for neither side: a Finglish question is not less Finglish for saying
// «check», and an English one is not more Finglish for it either.
var neutral = wordSet("hi", "hello", "ok", "okay", "oke", "pls", "plz", "please",
	"check", "status", "report", "list", "update", "thanks", "thx", "tnx")

func wordSet(ws ...string) map[string]bool {
	m := make(map[string]bool, len(ws))
	for _, w := range ws {
		m[canon(w)] = true
	}
	return m
}

// entry is one known Latin word.
type entry struct {
	fa       string
	distinct bool
}

// lexicon is both tables, keyed by canonical spelling, built once.
var lexicon = func() map[string]entry {
	m := make(map[string]entry, 700)
	load(m, distinctive, true)
	load(m, ambiguous, false)
	return m
}()

// load reads one table into m.
//
// «-» as the Persian means the word is dropped: an ezafe typed as a word of
// its own, «hafte ye gozashte». Persian script does not write it, and
// rendering it as «یه» would put a word between «هفته» and «گذشته» that no
// keyword has.
func load(m map[string]entry, table string, distinct bool) {
	for _, line := range strings.Split(table, "\n") {
		fa, latin, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		fa = strings.ReplaceAll(strings.TrimSpace(fa), "_", zwnj)
		if fa == "-" {
			fa = ""
		}
		for _, w := range strings.Fields(latin) {
			key := canon(w)
			if prev, dup := m[key]; dup {
				// The same Persian twice under spellings that canon brings
				// together is harmless; anything else is two readings of one
				// key, and one of them would silently win.
				if prev.fa != fa || prev.distinct != distinct {
					panic("finglish: " + w + " is both «" + prev.fa + "» and «" + fa + "»")
				}
				continue
			}
			m[key] = entry{fa: fa, distinct: distinct}
		}
	}
}

// phrases are two Latin words that Persian writes as one: «pish factor» is
// «پیش‌فاکتور», not «پیش فاکتور». Keyed by the two canonical spellings.
//
// Joined with a half-space because that is how Persian writes them, and
// fa.Fold treats a typed half-space and this one alike.
var phrases = func() map[string]string {
	m := map[string]string{}
	for _, p := range [][2]string{
		{"پیش_فاکتور", "pish factor|pish faktor"},
		{"ضمانت_نامه", "zemanat name|zamanat name"},
		{"تأمین_کننده", "tamin konande"},
		{"پیش_نویس", "pish nevis"},
		{"پیش_پرداخت", "pish pardakht"},
		{"پیش_دریافت", "pish daryaft"},
		{"صورت_حساب", "surat hesab"},
		{"سررسید", "sar resid"},
	} {
		fa := strings.ReplaceAll(p[0], "_", zwnj)
		for _, latin := range strings.Split(p[1], "|") {
			a, b, _ := strings.Cut(latin, " ")
			m[canon(a)+" "+canon(b)] = fa
		}
	}
	return m
}()
