package walarchive

import (
	"encoding/hex"
	"errors"
	"maps"
	"strconv"

	"github.com/NordTempo/tempoDock/internal/s3"
)

// Config is where a database's archive is and how to open it. Core
// passes it to the archiver and to jobs as environment variables (Env),
// never on a command line.
type Config struct {
	Bucket s3.Bucket
	// Prefix is the generation's folder (Prefix).
	Prefix string
	// Key is the database's key (ServiceKey), never the archive key
	// itself.
	Key []byte
}

// Environment variable names of a Config.
const (
	envEndpoint  = "DOCK_WAL_ENDPOINT"
	envRegion    = "DOCK_WAL_REGION"
	envBucket    = "DOCK_WAL_BUCKET"
	envPathStyle = "DOCK_WAL_PATH_STYLE"
	envAccessKey = "DOCK_WAL_ACCESS_KEY_ID"
	envSecretKey = "DOCK_WAL_SECRET_ACCESS_KEY"
	envPrefix    = "DOCK_WAL_PREFIX"
	envKey       = "DOCK_WAL_KEY"
)

// Secrets are the variables of Env that are secret, for masking job
// output.
var Secrets = []string{envSecretKey, envKey}

// Env is c as environment variables, added to env.
func (c Config) Env(env map[string]string) map[string]string {
	out := maps.Clone(env)
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, map[string]string{
		envEndpoint: c.Bucket.Endpoint, envRegion: c.Bucket.Region, envBucket: c.Bucket.Name,
		envPathStyle: strconv.FormatBool(c.Bucket.PathStyle),
		envAccessKey: c.Bucket.AccessKeyID, envSecretKey: c.Bucket.SecretAccessKey,
		envPrefix: c.Prefix, envKey: hex.EncodeToString(c.Key),
	})
	return out
}

// ConfigFromEnv reads a Config written by Env.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	key, err := hex.DecodeString(getenv(envKey))
	if err != nil || len(key) != KeySize {
		return Config{}, errors.New(envKey + " must be the archive key in hex")
	}
	c := Config{
		Bucket: s3.Bucket{
			Endpoint: getenv(envEndpoint), Region: getenv(envRegion), Name: getenv(envBucket),
			PathStyle: getenv(envPathStyle) == "true", AccessKeyID: getenv(envAccessKey), SecretAccessKey: getenv(envSecretKey),
		},
		Prefix: getenv(envPrefix), Key: key,
	}
	if c.Bucket.Endpoint == "" || c.Bucket.Name == "" || c.Prefix == "" {
		return Config{}, errors.New("the archive's bucket and folder are not set (" + envEndpoint + ", " + envBucket + ", " + envPrefix + ")")
	}
	return c, nil
}
