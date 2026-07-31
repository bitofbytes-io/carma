package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/bitofbytes-io/carma/internal/config"
	"github.com/bitofbytes-io/carma/internal/database"
	"github.com/bitofbytes-io/carma/migrations"
	"github.com/jackc/pgx/v5"
)

func main() {
	databaseURL, e := config.LoadDatabaseURL()
	if e != nil {
		log.Fatal(e)
	}
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn, e := pgx.Connect(ctx, databaseURL)
	if e != nil {
		log.Fatal(e)
	}
	defer conn.Close(ctx)
	if e = database.Migrate(ctx, conn, migrations.FS); e != nil {
		log.Fatal(e)
	}
	_, _ = os.Stdout.WriteString("migrations applied\n")
}
