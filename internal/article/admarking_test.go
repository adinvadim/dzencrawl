package article

import (
	"reflect"
	"testing"
)

func TestAdMarkingFindsDisclosureLines(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"footer with erid", "Текст.\n\nРеклама. ИП Саванеев В. В. ИНН 501004383504. erid: 2SDnjcUSQQQ\n\n## Пишите", []string{"Реклама. ИП Саванеев В. В. ИНН 501004383504. erid: 2SDnjcUSQQQ"}},
		{"inn only, no dot", "Реклама ООО \"Вест Трейд\" ИНН 7743150577", []string{"Реклама ООО \"Вест Трейд\" ИНН 7743150577"}},
		{"emphasis and escapes", "*Реклама\\. ООО «Айро»\\. ИНН 7703821839\\.*", []string{"Реклама. ООО «Айро». ИНН 7703821839."}},
		{"bold word only, CRLF", "**Реклама**. ООО «Ту Би Блоссом». ИНН 9729310938 erid: 2VtzqxGrMbc\r\n", []string{"Реклама. ООО «Ту Би Блоссом». ИНН 9729310938 erid: 2VtzqxGrMbc"}},
		{"two advertisers", "Реклама. ООО \"КУПИШУЗ\". ИНН 7705935687.\nРеклама. AO \"Мэлон Фэшн Груп\". ИНН 7839326623.", []string{"Реклама. ООО \"КУПИШУЗ\". ИНН 7705935687.", "Реклама. AO \"Мэлон Фэшн Груп\". ИНН 7839326623."}},
		{"denial is not marking", "В капсулах мы всех отправляем в Crazy Beach. Это не реклама.", []string{}},
		{"word inside a sentence", "Рекламная кампания бренда показала мальчика в тройке.", []string{}},
		{"bare word without advertiser", "Реклама\n\nдальше текст", []string{}},
	}
	for _, c := range cases {
		if got := AdMarking(c.body); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
