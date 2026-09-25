# Вектора RFC 4475 (SIP Torture Test Messages)

49 сообщений раздела 3 RFC 4475 — дословно, побайтно, из архива
приложения A самого RFC (`docs/sip-standards/rfc/RFC4475.txt`). Извлечены
по процедуре рисунка 58 документа: строки между `-- BEGIN MESSAGE ARCHIVE
--` и `-- END MESSAGE ARCHIVE --`, состоящие из одного слова, — base64
сжатого tar-архива.

```bash
python3 - <<'PY' | tar -xzf - -C /tmp/rfc4475
import re, base64, sys
inside, data = False, ""
for line in open("docs/sip-standards/rfc/RFC4475.txt", encoding="latin-1"):
    if "-- BEGIN MESSAGE ARCHIVE --" in line: inside = True
    if inside and re.match(r"^\s*[^\s]+\s*$", line): data += line
    if "-- END MESSAGE ARCHIVE --" in line: inside = False
sys.stdout.buffer.write(base64.b64decode(data))
PY
```

В архиве 50 файлов; `test.dat` ни в одном разделе RFC не описан, ожидания
к нему нет, и сюда он не положен.

**Откуда, а не из чужих тестов.** Те же сообщения лежат в тестах pjsip,
Kamailio и других стеков — под их лицензиями (GPL). Берутся из самого RFC
(`CLAUDE.md`, правило о референсных реализациях). В бинарник они не входят:
это данные проверок.

**Файлы не правятся.** Порча одного байта меняет смысл проверки:
`escnull` проверяет `%00`, `unreason` — UTF-8 в строке причины, `dblreq` —
вторую копию запроса в той же дейтаграмме. `.gitattributes` рядом
запрещает git нормализовать концы строк.

Ожидания по каждому файлу — раздел RFC и что обязан сделать узел — в
`rfc4475_settaswitch_test.go` (разборщик) и
`internal/engine/rfc4475_test.go` (ответ узла на проводе).
