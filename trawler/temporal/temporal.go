package temporal

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/spf13/viper"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/durationpb"
)

func ensureNamespace(ctx context.Context, opts client.Options, namespace string) error {
	log.Printf("ensuring '%s' namespace (will create if it does not exist)...", namespace)
	client, err := client.NewNamespaceClient(opts)
	if err != nil {
		return fmt.Errorf("could not create namespace client: %w", err)
	}

	duration := viper.GetDuration("temporal.retention")

	// TODO is this really necessary? I see code on GitHub just passing a
	// time.Duration instead of this durationpb.
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
	hostport := viper.GetString("temporal.hostport")
	if hostport == "" {
		hostport = client.DefaultHostPort
	}
	opts := client.Options{
		HostPort: hostport,
	}
	namespace := viper.GetString("temporal.namespace")
	if namespace != "" {
		err := ensureNamespace(ctx, opts, namespace)
		if err != nil {
			return nil, fmt.Errorf("could not ensure namespace: %w", err)
		}
	} else {
		namespace = "default"
	}

	opts.Namespace = namespace

	c, err := client.Dial(opts)
	if err != nil {
		log.Fatalln("Unable to create client", err)
	}
	return c, nil
}
