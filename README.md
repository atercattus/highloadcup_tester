# highloadcup_tester
Простой http клиент, который может выполнять тестирование решения для [mail.ru'шного HighLoad Cup](https://highloadcup.ru/round/1/) как на корректность ответов (статус и тело), так и "нагрузочно".


Для запуска теста нужно иметь локально склонированную репу `github.com/sat2707/hlcupdocs`, или хотя бы ее кусок `/hlcupdocs/data/{TRAIN,FULL}/`.

**Для второго капа (2018 год) нужны архивы с патронами по ссылкам вида https://highloadcup.ru/media/condition/test_accounts_141218.zip (из раздела "Тестовые данные" тут https://highloadcup.ru/ru/round/3/).**

**После распаковки запускать `./highloadcup_tester -hlcupdocs path/to/unpacked/zip/`, чтобы существовали пути `path/to/unpacked/zip/{ammo,answers}`.**

#### Сборка
```bash
go get -u github.com/AterCattus/highloadcup_tester
```
либо:
```bash
cd $GOPATH
git clone https://github.com/AterCattus/highloadcup_tester.git
cd highloadcup_tester
go get && CGO_ENABLED=0 go build -ldflags '-s -extldflags "-static"' -installsuffix netgo
```

#### Проверка корректности:
Для примера фаза 1, каждый запрос выполняется по одному разу.
```
./highloadcup_tester -addr http://127.0.0.1:8081 -hlcupdocs /path/to/hlcupdocs/FULL/ -test -phase 1
```

Для работы тестилки должны быть доступны (по FULL/data/{locations,users,visits}_*.json строится data.zip для самого тестируемого решения):
```
/path/to/hlcupdocs/FULL/ammo/phase_*_*.ammo
/path/to/hlcupdocs/FULL/answers/phase_*_*.answ
```

Проверяются:
* Статус ответа
* Тело ответа с его анализом. Не тупо строковое сравнение двух json, а все типы, значения, порядок в массивах, точность float... Если в ответах коррректно получать null, то нужно запускать тестер с флажком -allow-nulls.

#### Полный прогон всех трех фаз:
```
for p in {1..3}; do
    ./highloadcup_tester -addr http://127.0.0.1:8081 -hlcupdocs /path/to/hlcupdocs/FULL/ -test -phase $p
 done
```

#### Еще можно (но не нужно) потестить решение под нагрузкой:
Для примера 2 потока в течение 30 секунд будут долбиться в сервер. Все ответы при этом так же собираются и анализируются в конце
```
./highloadcup_tester -addr http://127.0.0.1:8081 -hlcupdocs /path/to/hlcupdocs/FULL/ -concurrent 2 -time 30s -phase 3
```

-----
P.S. Написано левой пяткой, могут быть баги и неточности, но мне сильно помогло :)

P.P.S. Если он жрет больше проца, чем тестируемое решение, то вам не показалось :)
