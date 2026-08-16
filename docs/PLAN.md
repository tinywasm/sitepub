---
PLAN: "feat: sitepub — publicar un sitio compilado a un remoto git, atómicamente"
EXECUTOR: jules
REVIEWER: none
STATUS: review
SESSION: 5654775287489933520
PR: https://github.com/tinywasm/sitepub/pull/1
---

> Este plan se despacha con el flujo CodeJob. Ver skill: agents-workflow.

# Plan — construir esta librería desde cero

El repo está recién creado (`gonew`): solo tiene `go.mod`, `README.md`,
`LICENSE` y un `sitepub.go` de plantilla. Todo lo de abajo es nuevo.

## Qué problema resuelve

Un CMS que edita contenido y publica un sitio estático necesita, cada vez:

```
1. tener una copia de trabajo del repo del sitio (clonar la 1ª vez, actualizar después)
2. escribir los ficheros de contenido generados
3. compilar el sitio con sitec
4. si el build falla → NO publicar
5. commit + push
6. si nada cambió → no es un error, es un no-op
```

Nada de eso es específico de ningún negocio, y **cada aplicación que publique
un sitio escribiría exactamente el mismo cableado**. Por la regla lego del
`CONSTRUCTION_HARNESS.md` —*"el pegamento se escribe una vez, en la librería
que lo posee"*— ese ciclo es esta pieza.

Vive en `tinywasm` y no en una organización de negocio porque no tiene una sola
semántica de dominio: sabe de git, de `sitec` y de ficheros. Y compone dos
librerías de `tinywasm` (`sitec` + `devflow`); si el pegamento entre dos piezas
del framework viviera fuera, un usuario del framework necesitaría una
dependencia ajena para obtener glue del framework.

El nombre empareja con `sitec` a propósito: **`sitec` compila un sitio,
`sitepub` lo publica.** Piezas hermanas de la misma historia.

## Lo que NO hace (y por qué)

- **No decide cuándo publicar.** Nada de temporizadores, colas ni "qué está
  sucio". Eso necesita persistencia y está atado a los eventos de dominio de
  cada aplicación; meter una abstracción de almacenamiento aquí sería scope
  creep. `sitepub` hace una cosa: **publicar ahora, atómicamente.** La
  aplicación decide cuándo.
- **No genera el contenido.** Recibe los ficheros ya generados. Qué consultar y
  cómo mapearlo a los tipos del layout es irreduciblemente específico de cada
  sitio.
- **No habla con ningún proveedor de hosting.** No hay API de Cloudflare, ni
  de Netlify, ni de nada. El push a git es el final del camino; quien sincroniza
  el repo con el hosting se configura una vez fuera de aquí.
- **No sabe de autenticación.** El transporte lo trae la URL del remoto. Con
  SSH (`git@github.com:…`), la clave la resuelve el agente/config de la
  máquina — que es justo lo que permite una *deploy key* acotada a un repo.

## La API

```go
package sitepub

type Author struct {
	Name  string // p. ej. "MJosefa CMS"
	Email string // p. ej. "cms@monjitaschillan.cl"
}

type Config struct {
	RepoURL string // git@github.com:org/site.git
	WorkDir string // copia de trabajo local
	Branch  string // vacío = la rama por defecto del remoto
	SiteDir string // subdirectorio del repo donde corre sitec; vacío = raíz
	Author  Author
}

// File es un fichero generado, con ruta relativa a la RAÍZ del repo.
type File struct {
	Path    string
	Content []byte
}

type Publisher struct{ /* no exportar los campos */ }

func New(cfg Config) (*Publisher, error)

func (p *Publisher) SetLog(fn func(...any))

// Publish corre el ciclo completo y es atómico respecto al remoto: o el
// sitio compila y se publica entero, o no se hace ningún commit.
func (p *Publisher) Publish(files []File, message string) (Result, error)

type Result struct {
	Published bool   // false cuando no había nada que cambiar
	Commit    string // sha del commit hecho; vacío si Published es false
	Summary   string
}
```

`Config` se valida en `New`, no en `Publish`: un `RepoURL` vacío es un error de
programación y debe salir en el arranque, no en el primer intento de publicar
a las 3 de la mañana.

## Las piezas que YA existen — no las reimplementes

Todo el trabajo con git está publicado en `github.com/tinywasm/devflow`
**v0.4.62**. Úsalo, no llames a `exec.Command("git", …)` por tu cuenta:

| Necesidad | Método |
|---|---|
| crear el handler | `devflow.NewGit()` |
| fijar la copia de trabajo | `(*Git).SetRootDir(path)` |
| clonar la 1ª vez | `(*Git).Clone(repoURL) (alreadyPresent bool, err error)` — idempotente |
| actualizar | `(*Git).Pull() error` — devuelve `devflow.ErrDirtyWorkTree` si el árbol está sucio |
| identidad del commit | `(*Git).SetUserConfig(name, email) error` |
| commitear rutas | `(*Git).CommitPaths(message, paths...) (bool, error)` — el bool es "hubo algo que commitear" |
| **empujar sin tag** | `(*Git).PushWithoutTags() (bool, error)` |

**Ojo con `(*Git).Push(message, tag)`: NO la uses.** Genera y empuja un *tag de
versión* — es el flujo de `gopush` para publicar módulos Go. Un sitio de
contenido no quiere una etiqueta de versión por cada edición de texto.
`PushWithoutTags` es el camino correcto.

Para compilar: `github.com/tinywasm/sitec` **v0.0.57**, que ya emite
multipágina, procesa imágenes y escribe a disco. La forma más simple y
honesta de invocarlo es **ejecutar el binario `sitec build`** como
subproceso en `SiteDir` — así el publicador obtiene el mismo artefacto que
obtendría un humano o un CI, y el fallo de build es un exit code, no un
estado interno que haya que interpretar. Antes de decidirlo, **lee
`sitec/cmd/sitec/main.go`** para ver qué flags acepta (`-o` para el
directorio de salida) y qué imprime (un manifiesto JSON por stdout; los
errores por stderr).

## Comportamiento que debe quedar clavado

- **El build ocurre ANTES del commit.** Si `sitec build` falla, `Publish`
  devuelve error y **no deja ningún commit** — ni local. Un sitio roto no llega
  al repositorio, menos aún al hosting. Es la razón principal de que esta pieza
  exista en vez de dejar que un CI construya después del push.
- **"Nada cambió" es un `Result`, no un error.** Si la aplicación publica cada
  N minutos y nadie editó, `Published: false` y listo. Devolver error ahí llena
  el log de fallos que nadie lee, y entrena a los operadores a ignorarlos.
- **`Clone` se llama incondicionalmente al arrancar.** Ya es idempotente; no
  preguntes primero si existe.
- **Un árbol sucio no se pisa.** Si `Pull` devuelve `ErrDirtyWorkTree`,
  propágalo con contexto: significa que alguien dejó cambios a mano en la copia
  de trabajo, y adivinar (descartar, o commitearlos junto al contenido) sería
  peor que parar.
- **Los ficheros generados se escriben, no se mezclan.** `File.Content`
  reemplaza el fichero entero.

## Restricciones

- Esta librería es herramienta de backend: usa la biblioteca estándar
  legítimamente (`os`, `os/exec`, `path/filepath`). No "arregles" esos imports.
- Sin carpetas `internal/`.
- Todo string repetido es una constante con nombre.
- Superficie mínima (principio 5): exporta `Config`, `Author`, `File`,
  `Result`, `Publisher`, `New`, `SetLog`, `Publish`. Nada más. Lo que no se ve,
  no se puede usar mal.
- Si a `devflow` o a `sitec` les falta algo para servir aquí, **dilo en el
  PR** — se arregla allá y se publica; no lo envuelvas ni lo copies.

## Verificación

La regla del arnés: **una API no está publicada hasta que un test con forma de
consumidor, dentro de la librería, la prueba.**

Los tests usan repos git **locales de verdad** (`git init --bare` en un
`t.TempDir()` como remoto, y clonar por ruta de sistema de ficheros) — sin red
y sin dobles: esto envuelve al binario `git`, y un doble no probaría nada. Es el
mismo criterio con el que se probaron `Clone`/`Pull` en `devflow`.

Casos que deben quedar cubiertos:

- **Ciclo completo en limpio**: repo remoto vacío → `Publish` con un fichero →
  el remoto tiene el commit, con el contenido y el autor configurado.
- **Segunda publicación sin cambios**: `Published == false`, y **el remoto no
  recibe ningún commit nuevo**.
- **Segunda publicación con cambios**: solo un commit nuevo, con el contenido
  actualizado.
- **Build fallido**: un `SiteDir` cuyo `sitec build` falla (p. ej. fuente Go que
  no compila) → `Publish` devuelve error **y el repo local no tiene commits
  nuevos**. Ésta es la aserción más importante del plan.
- **Árbol sucio**: dejar un cambio a mano en la copia de trabajo → `Publish`
  falla con un error que envuelve `devflow.ErrDirtyWorkTree`, sin pisar nada.
- **`Clone` idempotente**: dos `Publish` seguidos sobre el mismo `WorkDir` no
  fallan por "ya existe".
- **`New` valida**: `Config` sin `RepoURL` o sin `WorkDir` → error en `New`.

`go build ./... && go vet ./... && go test ./...` verde.

## Etapas

| # | Alcance | Aceptación |
|---|---|---|
| 1 | `Config`, `Author`, `File`, `Result`, `New` con validación | `New` rechaza config incompleta; tests de validación |
| 2 | Copia de trabajo: `Clone` idempotente + `Pull` + `SetUserConfig` | dos arranques seguidos funcionan; árbol sucio falla limpio |
| 3 | Escribir ficheros + invocar `sitec build` | build correcto produce salida; build fallido devuelve error |
| 4 | Commit + `PushWithoutTags`, con el no-op de "nada cambió" | ciclo completo contra un remoto local; sin tags creados |
| 5 | Suite completa con repos git reales | los 7 casos de Verificación en verde |
