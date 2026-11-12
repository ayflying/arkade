# PostgreSQL GoFrame generation

Do not hand-edit DAO/DO/entity files. Use the official gf CLI **v2.9.6** with
GF pgsql **v2.9.6**. This tool migrates a dedicated PostgreSQL database using
`dbmigrate.Run`, discovers all public business tables (excluding migration
metadata), runs official code generation in an isolated temporary module, then
copies only the generated Go files into the hub. Custom files are preserved.

Create an empty, disposable PostgreSQL database first. The migration role must
own public objects and have database CREATE privilege for rollback-only schema
verification. Never point generation at production; the tool does not create,
reset, or drop its target database. Set a URL (not a keyword DSN) for this tool:

```powershell
$env:GFGEN_DSN = 'postgres://user:password@localhost:5432/arkade_codegen?sslmode=disable'
$env:GF_BIN = 'D:\gopath\gf296\gf.exe'
pwsh ./tools/gfgen/gen.ps1
```

```sh
export GFGEN_DSN='postgres://user:password@localhost:5432/arkade_codegen?sslmode=disable'
export GF_BIN="$HOME/gopath/gf296/gf"
bash ./tools/gfgen/gen.sh
```

Run from hub. The target DSN is not printed. A helper renders the PostgreSQL URL into GF's
structured ConfigNode fields in a temporary JSON configuration (GF 2.9.6 Link
syntax does not accept native PostgreSQL URLs). You can also migrate/discover separately:

```sh
go run ./tools/gfgen/mkgen "$GFGEN_DSN"
go run ./tools/gfgen/mktables "$GFGEN_DSN"
```

The isolation avoids the previous gf CLI panic encountered in the full hub
module. It is a workaround, not a claim that the underlying CLI bug was fixed.
