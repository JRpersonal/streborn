# STR website copy: Alexa / voice control FAQ entry (2026-08-11)

Handoff for the web team (st-reborn.de). One new FAQ entry, one section per UI
language, identical structure. German is canonical, the others are faithful
translations of it.

**Where it goes:** the FAQ page, in the "what STR can and cannot do" area, near
the entries about what stopped working with the Bose cloud.

**Please keep:** the "no" is deliberately in the first sentence. People arrive at
this question expecting a yes, and burying the answer under an explanation reads
as evasive. The link at the end points to the written setup guide; the app links
to the same page from the speaker settings.

**Link target:** https://github.com/JRpersonal/streborn/blob/main/docs/ALEXA.md
(replace with the website's own page if this content is ever hosted there, and
tell me so the app's link can follow).

**Note on the trademark line:** it is short on purpose and should not be dropped.
Alexa and Amazon Echo are Amazon's trademarks, Home Assistant belongs to its
project, and STR is affiliated with neither.

---

## Deutsch (de) - canonical

**Frage:** Kann ich meine Lautsprecher mit Alexa steuern?

**Antwort:**
Nicht direkt, aber es geht mit einem kleinen Umweg.

Die frühere Alexa-Steuerung lief über die Bose-Cloud: Alexa hat mit Bose
gesprochen, Bose mit deinem Lautsprecher. Beide Hälften sind abgeschaltet, und
deshalb lässt sich der alte Alexa-Skill nicht wiederbeleben, von niemandem.

Einen neuen Skill bauen wir bewusst nicht. Der bräuchte einen Server im
Internet, ein Konto und laufende Kosten, und würde eine Tür von außen zu deinem
Lautsprecher öffnen. STR läuft absichtlich nur bei dir zu Hause, ohne Konto und
ohne Cloud.

Was funktioniert: eine Steuerzentrale in deinem eigenen Netzwerk, zum Beispiel
Home Assistant. Sie spricht mit deinen Lautsprechern und meldet sie bei Alexa
an. Damit kannst du Lautsprecher ein- und ausschalten, die Lautstärke ändern
und deine Presets per Sprache starten, etwa "Alexa, schalte Küche Preset 2
ein". Freie Sätze wie "spiel Sender X" sind damit nicht möglich.

Das ist etwas für alle, die sich gern einen kleinen Server einrichten. Wenn dir
das zu viel ist, verlierst du nichts: die App und die Handy-Fernbedienung
steuern alles auch ohne das.

**Anleitung:** [Sprachsteuerung einrichten](LINK)

*Alexa und Amazon Echo sind Marken von Amazon, Home Assistant ist eine Marke des
Home-Assistant-Projekts. STR steht mit keinem von beiden in Verbindung.*

---

## English (en)

**Question:** Can I control my speakers with Alexa?

**Answer:**
Not directly, but there is a way round.

The old Alexa control ran through the Bose cloud: Alexa talked to Bose, and Bose
talked to your speaker. Both halves are switched off, which is why the old Alexa
skill cannot be brought back, by anyone.

We deliberately do not build a new one. It would need a server on the internet,
an account and running costs, and it would open a door to your speaker from
outside. STR runs at your home only, with no account and no cloud.

What does work is a hub on your own network, Home Assistant for example. It
talks to your speakers and offers them to Alexa. That gives you switching
speakers on and off, changing the volume, and starting your presets by voice,
for example "Alexa, turn on kitchen preset 2". Free speech like "play station X"
is not part of it.

This is for anyone happy to run a small server. If that is not for you, you lose
nothing: the app and the phone remote control everything without it.

**Guide:** [Setting up voice control](LINK)

*Alexa and Amazon Echo are trademarks of Amazon, Home Assistant is a trademark of
the Home Assistant project. STR is affiliated with neither.*

---

## Español (es)

**Pregunta:** ¿Puedo controlar mis altavoces con Alexa?

**Respuesta:**
No directamente, pero hay un camino.

El antiguo control por Alexa funcionaba a través de la nube de Bose: Alexa
hablaba con Bose y Bose con tu altavoz. Las dos mitades están apagadas, por eso
nadie puede revivir la antigua skill de Alexa.

No vamos a crear una nueva, y es una decisión consciente. Necesitaría un
servidor en internet, una cuenta y costes permanentes, y abriría una puerta a tu
altavoz desde fuera. STR funciona solo en tu casa, sin cuenta y sin nube.

Lo que sí funciona es una central en tu propia red, por ejemplo Home Assistant.
Habla con tus altavoces y se los ofrece a Alexa. Así puedes encender y apagar
altavoces, cambiar el volumen e iniciar tus presets por voz, por ejemplo "Alexa,
enciende cocina preset 2". Frases libres como "pon la emisora X" no son
posibles.

Esto es para quien no le importe montar un pequeño servidor. Si no es tu caso,
no pierdes nada: la aplicación y el mando del móvil lo controlan todo sin eso.

**Guía:** [Configurar el control por voz](LINK)

*Alexa y Amazon Echo son marcas de Amazon, Home Assistant es una marca del
proyecto Home Assistant. STR no está afiliado a ninguno de los dos.*

---

## Français (fr)

**Question :** Puis-je commander mes enceintes avec Alexa ?

**Réponse :**
Pas directement, mais il existe un détour.

L'ancienne commande Alexa passait par le cloud Bose : Alexa parlait à Bose, et
Bose à votre enceinte. Les deux moitiés sont éteintes, c'est pourquoi personne
ne peut ressusciter l'ancienne skill Alexa.

Nous n'en construisons pas de nouvelle, et c'est un choix. Il faudrait un
serveur sur internet, un compte et des frais permanents, et cela ouvrirait une
porte vers votre enceinte depuis l'extérieur. STR fonctionne uniquement chez
vous, sans compte et sans cloud.

Ce qui fonctionne, c'est une centrale sur votre propre réseau, Home Assistant
par exemple. Elle parle à vos enceintes et les propose à Alexa. Vous pouvez
alors allumer et éteindre une enceinte, régler le volume et lancer vos
présélections à la voix, par exemple « Alexa, allume cuisine preset 2 ». Les
phrases libres comme « joue la radio X » ne sont pas possibles.

C'est pour celles et ceux que l'installation d'un petit serveur ne rebute pas.
Sinon, vous ne perdez rien : l'application et la télécommande sur mobile
commandent tout sans cela.

**Guide :** [Configurer la commande vocale](LINK)

*Alexa et Amazon Echo sont des marques d'Amazon, Home Assistant est une marque du
projet Home Assistant. STR n'est affilié à aucun des deux.*

---

## Nederlands (nl)

**Vraag:** Kan ik mijn speakers met Alexa bedienen?

**Antwoord:**
Niet rechtstreeks, maar er is een omweg.

De oude Alexa-bediening liep via de Bose-cloud: Alexa praatte met Bose en Bose
met je speaker. Beide helften zijn uitgezet, en daarom kan niemand de oude
Alexa-skill terugbrengen.

Een nieuwe bouwen we bewust niet. Die zou een server op internet nodig hebben,
een account en doorlopende kosten, en zou van buitenaf een deur naar je speaker
openen. STR draait alleen bij jou thuis, zonder account en zonder cloud.

Wat wel werkt is een centrale in je eigen netwerk, bijvoorbeeld Home Assistant.
Die praat met je speakers en biedt ze aan Alexa aan. Daarmee kun je speakers aan
en uit zetten, het volume wijzigen en je presets met je stem starten,
bijvoorbeeld "Alexa, zet keuken preset 2 aan". Vrije zinnen als "speel zender X"
zijn niet mogelijk.

Dit is voor wie een kleine server wil draaien. Is dat niets voor jou, dan verlies
je niets: de app en de telefoonafstandsbediening bedienen alles ook zonder.

**Handleiding:** [Spraakbediening instellen](LINK)

*Alexa en Amazon Echo zijn merken van Amazon, Home Assistant is een merk van het
Home Assistant-project. STR is aan geen van beide verbonden.*

---

## Polski (pl)

**Pytanie:** Czy mogę sterować głośnikami przez Alexę?

**Odpowiedź:**
Nie bezpośrednio, ale jest obejście.

Dawne sterowanie przez Alexę działało przez chmurę Bose: Alexa rozmawiała z
Bose, a Bose z Twoim głośnikiem. Obie połowy są wyłączone, dlatego nikt nie
przywróci starej umiejętności Alexy.

Nowej świadomie nie budujemy. Wymagałaby serwera w internecie, konta i stałych
kosztów, a także otworzyłaby drzwi do Twojego głośnika z zewnątrz. STR działa
wyłącznie u Ciebie w domu, bez konta i bez chmury.

Działa natomiast centrala w Twojej własnej sieci, na przykład Home Assistant.
Rozmawia z głośnikami i udostępnia je Alexie. Możesz wtedy włączać i wyłączać
głośniki, zmieniać głośność i uruchamiać swoje presety głosem, na przykład
"Alexa, włącz kuchnia preset 2". Swobodne zdania w rodzaju "włącz stację X" nie
są możliwe.

To rozwiązanie dla osób, którym nie przeszkadza postawienie małego serwera. Jeśli
to nie Ty, nic nie tracisz: aplikacja i pilot w telefonie obsługują wszystko bez
tego.

**Przewodnik:** [Konfiguracja sterowania głosem](LINK)

*Alexa i Amazon Echo są znakami towarowymi Amazona, Home Assistant jest znakiem
towarowym projektu Home Assistant. STR nie jest powiązany z żadnym z nich.*

---

## Lietuvių (lt)

**Klausimas:** Ar galiu valdyti garsiakalbius su „Alexa“?

**Atsakymas:**
Ne tiesiogiai, bet yra apylankos kelias.

Senasis „Alexa“ valdymas veikė per „Bose“ debesį: „Alexa“ kalbėdavosi su „Bose“,
o „Bose“ su jūsų garsiakalbiu. Abi pusės išjungtos, todėl senojo „Alexa“ įgūdžio
nebeatgaivins niekas.

Naujo sąmoningai nekuriame. Jam reikėtų serverio internete, paskyros ir
nuolatinių išlaidų, be to, jis atvertų duris į jūsų garsiakalbį iš išorės. STR
veikia tik pas jus namuose, be paskyros ir be debesies.

Veikia kitas kelias: valdymo centras jūsų pačių tinkle, pavyzdžiui Home
Assistant. Jis bendrauja su garsiakalbiais ir pasiūlo juos „Alexa“. Taip galite
įjungti ir išjungti garsiakalbius, keisti garsumą ir balsu paleisti savo
išsaugotas stotis, pavyzdžiui „Alexa, įjunk virtuvė preset 2“. Laisvi sakiniai
kaip „paleisk stotį X“ neveiks.

Tai skirta tiems, kam nesunku pasileisti nedidelį serverį. Jei tai ne jums,
nieko neprarandate: programa ir telefono pultas viską valdo ir be to.

**Vadovas:** [Valdymo balsu nustatymas](LINK)

*„Alexa“ ir „Amazon Echo“ yra „Amazon“ prekių ženklai, Home Assistant yra Home
Assistant projekto prekių ženklas. STR nesusijęs nė su vienu iš jų.*

---

## Latviešu (lv)

**Jautājums:** Vai varu vadīt skaļruņus ar Alexa?

**Atbilde:**
Ne tieši, bet ir apkārtceļš.

Vecā Alexa vadība darbojās caur Bose mākoni: Alexa runāja ar Bose, un Bose ar
jūsu skaļruni. Abas puses ir izslēgtas, tāpēc veco Alexa prasmi neviens vairs
neatdzīvinās.

Jaunu mēs apzināti neveidojam. Tai vajadzētu serveri internetā, kontu un
pastāvīgas izmaksas, un tā atvērtu durvis uz jūsu skaļruni no ārpuses. STR
darbojas tikai pie jums mājās, bez konta un bez mākoņa.

Darbojas gan centrāle jūsu pašu tīklā, piemēram, Home Assistant. Tā sarunājas ar
jūsu skaļruņiem un piedāvā tos Alexa. Tā varat ieslēgt un izslēgt skaļruņus,
mainīt skaļumu un ar balsi palaist savus iestatījumus, piemēram "Alexa, ieslēdz
virtuve preset 2". Brīvi teikumi kā "atskaņo staciju X" nav iespējami.

Tas ir domāts tiem, kam nesagādā grūtības uzstādīt nelielu serveri. Ja tas nav
priekš jums, jūs neko nezaudējat: lietotne un telefona pults vada visu arī bez
tā.

**Pamācība:** [Balss vadības uzstādīšana](LINK)

*Alexa un Amazon Echo ir Amazon preču zīmes, Home Assistant ir Home Assistant
projekta preču zīme. STR nav saistīts ne ar vienu no tiem.*

---

## Türkçe (tr)

**Soru:** Hoparlörlerimi Alexa ile kontrol edebilir miyim?

**Cevap:**
Doğrudan değil, ama bir yolu var.

Eski Alexa kontrolü Bose bulutu üzerinden çalışıyordu: Alexa Bose ile, Bose da
hoparlörünüzle konuşuyordu. İki yarı da kapatıldı, bu yüzden eski Alexa
becerisini kimse geri getiremez.

Yenisini bilerek yapmıyoruz. Bunun için internette bir sunucu, bir hesap ve
sürekli masraf gerekirdi ve hoparlörünüze dışarıdan bir kapı açardı. STR yalnızca
evinizde çalışır, hesapsız ve bulutsuz.

Çalışan yol şu: kendi ağınızdaki bir merkez, örneğin Home Assistant. Hoparlörlerinizle
konuşur ve onları Alexa'ya sunar. Böylece hoparlörleri açıp kapatabilir, sesi
değiştirebilir ve kayıtlı istasyonlarınızı sesle başlatabilirsiniz, örneğin
"Alexa, mutfak preset 2'yi aç". "X istasyonunu çal" gibi serbest cümleler mümkün
değildir.

Bu, küçük bir sunucu kurmaktan çekinmeyenler içindir. Size göre değilse hiçbir
şey kaybetmezsiniz: uygulama ve telefondaki kumanda her şeyi bunsuz da yönetir.

**Kılavuz:** [Sesle kontrolü kurma](LINK)

*Alexa ve Amazon Echo, Amazon'un ticari markalarıdır, Home Assistant ise Home
Assistant projesinin ticari markasıdır. STR bunların hiçbiriyle bağlantılı
değildir.*

---

## Українська (uk)

**Запитання:** Чи можу я керувати колонками через Alexa?

**Відповідь:**
Не напряму, але обхідний шлях є.

Старе керування через Alexa працювало через хмару Bose: Alexa спілкувалася з
Bose, а Bose з вашою колонкою. Обидві половини вимкнено, тому старий навик Alexa
ніхто вже не відновить.

Нового ми свідомо не робимо. Він потребував би сервера в інтернеті, облікового
запису та постійних витрат, а також відчинив би двері до вашої колонки ззовні.
STR працює лише у вас удома, без облікового запису та без хмари.

Що працює: центр у вашій власній мережі, наприклад Home Assistant. Він
спілкується з колонками і пропонує їх Alexa. Так ви зможете вмикати та вимикати
колонки, змінювати гучність і запускати збережені станції голосом, наприклад
"Alexa, увімкни кухня preset 2". Вільні речення на кшталт "увімкни станцію X"
неможливі.

Це для тих, кому не важко підняти невеликий сервер. Якщо ні, ви нічого не
втрачаєте: застосунок і пульт у телефоні керують усім і без цього.

**Посібник:** [Налаштування керування голосом](LINK)

*Alexa та Amazon Echo є торговими марками Amazon, Home Assistant є торговою
маркою проєкту Home Assistant. STR не пов'язаний із жодним із них.*

---

## 日本語 (ja)

**質問:** スピーカーを Alexa で操作できますか。

**回答:**
直接はできませんが、回り道があります。

以前の Alexa 操作は Bose のクラウドを経由していました。Alexa が Bose と話し、
Bose がスピーカーと話す仕組みです。その両方が停止したため、以前の Alexa スキル
は誰にも復活させられません。

新しいスキルを作る予定はありません。インターネット上のサーバー、アカウント、
継続的な費用が必要になり、外部からスピーカーへの入口を開くことにもなります。
STR はご自宅の中だけで動き、アカウントもクラウドも使いません。

使えるのは、ご自宅のネットワークにあるハブです。たとえば Home Assistant が
スピーカーと通信し、それを Alexa に提供します。これでスピーカーのオンとオフ、
音量の変更、プリセットの再生を音声で行えます。たとえば「アレクサ、キッチン
プリセット 2 をオンにして」です。「駅 X をかけて」のような自由な言い方には
対応しません。

小さなサーバーを自分で用意できる方向けの方法です。難しければ気にする必要は
ありません。アプリとスマートフォンのリモコンがあれば、すべて操作できます。

**ガイド:** [音声操作の設定](LINK)

*Alexa および Amazon Echo は Amazon の商標、Home Assistant は Home Assistant
プロジェクトの商標です。STR はいずれとも関係ありません。*

---

## العربية (ar)

**السؤال:** هل يمكنني التحكم في مكبرات الصوت عبر Alexa؟

**الجواب:**
ليس مباشرة، لكن هناك طريق بديل.

كان التحكم القديم عبر Alexa يمر بسحابة Bose: تتحدث Alexa إلى Bose، وتتحدث Bose
إلى مكبر الصوت لديك. وقد أُوقف الطرفان، ولهذا لا يستطيع أحد إعادة إحياء مهارة
Alexa القديمة.

ولن نبني مهارة جديدة، وهذا قرار مقصود. فهي تحتاج خادمًا على الإنترنت وحسابًا
وتكاليف مستمرة، وتفتح بابًا إلى مكبر صوتك من الخارج. يعمل STR داخل بيتك فقط،
بلا حساب وبلا سحابة.

ما ينجح هو وحدة مركزية داخل شبكتك، مثل Home Assistant. تتحدث مع مكبرات الصوت
وتعرضها على Alexa. عندها يمكنك تشغيل مكبرات الصوت وإيقافها وتغيير مستوى الصوت
وتشغيل محطاتك المحفوظة بالصوت، مثل "أليكسا، شغّل المطبخ preset 2". أما الجمل
الحرة مثل "شغّل المحطة X" فغير ممكنة.

هذا مناسب لمن لا يمانع تشغيل خادم صغير. وإن لم يكن مناسبًا لك فلن تخسر شيئًا:
التطبيق والريموت على الهاتف يتحكمان بكل شيء بدونه.

**الدليل:** [إعداد التحكم بالصوت](LINK)

*Alexa وAmazon Echo علامتان تجاريتان لشركة Amazon، وHome Assistant علامة تجارية
لمشروع Home Assistant. لا ترتبط STR بأي منهما.*
