package llm

// PersianStyle is the writing rule every prompt that asks for Persian ends
// with, so what a model writes reads like the rest of the application.
//
// The rules are the mechanical ones of the persian-writing guide
// (github.com/ali2000hos/persian-writing) -- the half-space, Persian letters
// and digits, Persian punctuation, no dashes -- and the two register habits
// that most mark a text as machine-written: the form-letter verbs and the
// stock constructions: the standard an interface's own text is held to,
// applied to the text the interface did not write.
const PersianStyle = "نگارش: فارسی رسمی ولی طبیعی؛ «است» بنویس نه «می‌باشد». " +
	"نیم‌فاصله در «می‌شود»، «نمی‌کند» و «سفارش‌ها» رعایت شود. " +
	"حرف‌های «ی» و «ک» فارسی و رقم‌ها فارسی باشند. " +
	"از «،» «؛» «؟» و «گیومه» استفاده کن و خط تیره (\u2014) ننویس. " +
	"قالب‌های کلیشه‌ای مثل «نه تنها… بلکه»، «در راستای» و «لازم به ذکر است» ننویس."
