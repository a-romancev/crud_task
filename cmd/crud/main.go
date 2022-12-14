package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/a-romancev/crud_task/auth"
	"github.com/a-romancev/crud_task/company"
	"github.com/a-romancev/crud_task/internal/event"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	var confPath string
	flag.StringVar(&confPath, "conf", ".", "PATH to config folder")
	flag.Parse()

	var conf Config
	conf = conf.WithFile(confPath)
	err := conf.Validate()
	if err != nil {
		log.Fatal().Err(err).Msg("Config validation error.")
	}

	level, err := zerolog.ParseLevel(conf.LogLevel)
	if err != nil {
		log.Fatal().Msgf("Unknown log level -- %s.", conf.LogLevel)
	}
	ctx := context.Background()
	ctx = log.Logger.Level(level).WithContext(ctx)
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pk, err := auth.NewPublicKey(conf.PublicKey)
	if err != nil {
		log.Fatal().Msg("Failed to parse public key.")
	}

	mongoClient, err := mongo.Connect(ctx, options.Client().ApplyURI(fmt.Sprintf(
		"mongodb://%s:%s@%s/%s",
		conf.Mongo.User,
		conf.Mongo.Password,
		conf.Mongo.Host,
		conf.Mongo.Database,
	)))
	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("Failed to connect to mongo.")
	}
	mongoDB := mongoClient.Database("companies")
	companyMongo := company.NewMongo(mongoDB)

	producer := event.NewProducer(event.KafkaConf{
		Topic:   conf.Kafka.Topic,
		Servers: conf.Kafka.Servers,
	})

	webServer := &http.Server{
		Addr:    conf.ListenWebAddress,
		Handler: NewHandler(companyMongo, producer, pk),
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}

	go func() {
		log.Ctx(ctx).Info().Msgf("Web listening on %s.", conf.ListenWebAddress)
		err = webServer.ListenAndServe()
		if err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return
			}
			log.Ctx(ctx).Fatal().Err(err).Msg("Web server failed.")
		}
	}()

	<-ctx.Done()
	log.Ctx(ctx).Info().Msg("Shutting down.")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = webServer.Shutdown(shutdownCtx)
	log.Ctx(ctx).Info().Msg("Shutdown complete.")
}
