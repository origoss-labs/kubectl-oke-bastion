# Effective Go — cheat sheet

Source: https://go.dev/doc/effective_go · fetched 2026-06-03 · condensed.

## Naming

- **Packages**: lower case, single word, no underscores; name = base dir. Avoid
  stutter: `bufio.Reader`, not `bufio.BufReader`.
- **Exported** = uppercase first letter. **MixedCaps**, never underscores.
- **Interfaces**: one-method → method + `-er` (`Reader`, `Writer`). Canonical
  method names: `Read`, `Write`, `Close`, `Flush`, `String`.
- **Getters**: no `Get` prefix — field `owner` → `Owner()` / `SetOwner()`.

## Formatting

- `gofmt` / `go fmt` is law: tabs for indent, brace on same line as control stmt,
  no parens around `if`/`for`/`switch` conditions. No line-length limit.

## Control flow

```go
if err := file.Chmod(0664); err != nil {   // if with init
    return err
}

f, err := os.Open(name)                     // omit unnecessary else
if err != nil {
    return err
}
useFile(f)

for key, value := range m { }               // range
for { }                                      // infinite

switch {                                     // expression-less switch
case '0' <= c && c <= '9': return c - '0'
case 'a' <= c && c <= 'f': return c - 'a' + 10
}

switch t := t.(type) {                       // type switch
case bool:   ...
case int:    ...
default:     fmt.Printf("unexpected %T\n", t)
}
```

## Functions & returns

```go
func (file *File) Write(b []byte) (n int, err error)   // multiple returns

func nextInt(b []byte, pos int) (value, nextPos int) { // named results
    ...
    return                                              // naked return
}
```

## defer (LIFO, runs at function return)

```go
f, err := os.Open(name)
if err != nil { return err }
defer f.Close()   // runs on every return path
```

## Allocation

- `new(T)` → zeroed `*T` (zero value should be usable).
- `make(T, ...)` → initialised slice / map / channel only.
- Composite literals: `return &File{fd: fd, name: name}`.

## Slices / maps

```go
x = append(x, 4, 5, 6)
x = append(x, y...)

if seconds, ok := timeZone[tz]; ok { ... }  // comma-ok
delete(timeZone, "PDT")                       // safe if absent
```

## Interfaces, methods, embedding

- Implement an interface by having its methods; no `implements` keyword.
- **Pointer receiver** when the method mutates the receiver; value receiver
  otherwise. Compiler auto-takes `&b` for addressable values.
- Compile-time interface check: `var _ json.Marshaler = (*RawMessage)(nil)`.
- Struct embedding promotes the embedded type's methods (composition over
  inheritance):

```go
type ReadWriter struct {
    *Reader
    *Writer
}
```

## Errors, panic, recover

- Errors are values — return and check, don't throw.
- Custom error type implements `Error() string`. Inspect with type assertion or
  `errors.As`/`errors.Is` (stdlib).
- `panic` only for truly unrecoverable state; `recover` only inside a deferred
  func, to turn a panic at a boundary into an error.

```go
func safelyDo(work *Work) {
    defer func() {
        if err := recover(); err != nil {
            log.Println("work failed:", err)
        }
    }()
    do(work)
}
```

## Concurrency — "share memory by communicating"

```go
go work()                          // goroutine

ci := make(chan int)               // unbuffered (sync)
cs := make(chan int, 100)          // buffered

c <- 1                             // send
<-c                                // receive (blocks)

for r := range queue { process(r) } // range over channel

select {                           // non-blocking with default
case b = <-freeList:
default:
    b = new(Buffer)
}

var sem = make(chan int, max)      // semaphore via buffered chan
sem <- 1; process(r); <-sem
```

Relevant to this project's supervisor: a `context.Context` to cancel the loop on
SIGTERM, a goroutine running the SSH forward whose error is delivered on a
channel, and `select` over {forward error, ctx.Done(), health-tick} to drive
redial/recreate.

## Printing verbs

`%v` default · `%+v` field names · `%#v` Go syntax · `%T` type · `%q` quoted ·
`%d` int · `%s` string. Custom `String() string` controls a type's print form.

## Blank identifier

```go
_, err := os.Stat(path)
import _ "net/http/pprof"          // side-effect import
var _ json.Marshaler = (*T)(nil)   // assert interface satisfaction
```
