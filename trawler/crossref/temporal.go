package crossref

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/spf13/viper"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/durationpb"
)

// TODO extract out of crossref package

func ensureNamespace(ctx context.Context, namespace string) error {
	log.Printf("ensuring '%s' namespace (will create if it does not exist)...", namespace)
	client, err := client.NewNamespaceClient(client.Options{
		HostPort: client.DefaultHostPort,
	})
	if err != nil {
		return fmt.Errorf("could not create namespace client: %w", err)
	}

	// TODO is this really necessary? I see code on GitHub just passing a time.Duration instead of this durationpb.
	duration, err := time.ParseDuration(viper.GetString("crossref.temporal_namespace_retention"))
	if err != nil {
		return fmt.Errorf("could not parse crossref.temporal_namespace_retention: %w", err)
	}

	dpb := &durationpb.Duration{
		Seconds: int64(duration.Seconds()),
	}

	err = client.Register(ctx, &workflowservice.RegisterNamespaceRequest{
		Namespace:                        namespace,
		WorkflowExecutionRetentionPeriod: dpb,
	})
	var namespaceExistsError *serviceerror.NamespaceAlreadyExists
	if err != nil && !errors.As(err, &namespaceExistsError) {
		return fmt.Errorf("could not register namespace '%s': %w", namespace, err)
	}

	return nil
}

// SetupTemporal creates a Temporal client, ensuring that the crossref namespace exists.
func SetupTemporal(ctx context.Context) (client.Client, error) {
	namespace := viper.GetString("crossref.temporal_namespace")
	if namespace != "" {
		err := ensureNamespace(ctx, namespace)
		if err != nil {
			return nil, fmt.Errorf("could not ensure namesapce: %w", err)
		}
	} else {
		namespace = "default"
	}
	hostport := viper.GetString("temporal.hostport")
	if hostport == "" {
		hostport = client.DefaultHostPort
	}

	c, err := client.Dial(client.Options{
		HostPort:  hostport,
		Namespace: namespace,
	})
	if err != nil {
		log.Fatalln("Unable to create client", err)
	}
	return c, nil
}
