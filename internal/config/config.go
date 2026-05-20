package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// StorageType defines the backend storage type.
type StorageType string

const (
	StorageLocal StorageType = "local"
	StorageS3    StorageType = "s3"
	StorageGCS   StorageType = "gcs"
	StorageAzure StorageType = "azure"
)

// FolderPermission represents a single folder permission entry.
// Format: /path:users:mode
//   - users: "public", "*", "alice bob", "* !charlie"
//   - mode:  "ro" | "rw"
type FolderPermission struct {
	Path     string   // e.g. "/public"
	Users    []string // e.g. ["alice", "bob"] or ["*"] or ["public"]
	Excluded []string // users prefixed with ! e.g. ["charlie"]
	Mode     string   // "ro" or "rw"
}

// LDAPConfig holds LDAP connection settings.
type LDAPConfig struct {
	URL          string
	BaseDN       string
	BindDN       string
	BindPassword string
	Attribute    string // e.g. "uid" or "sAMAccountName"
	StartTLS     bool
}

// OIDCConfig holds OpenID Connect settings.
type OIDCConfig struct {
	ProviderURL  string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
	UsernameClaim string
}

// Config is the full application configuration.
type Config struct {
	// Server
	ServerPort string

	// Storage
	StorageType StorageType
	// Local
	LocalDataPath string
	// S3
	S3Bucket    string
	S3Region    string
	S3Prefix    string
	S3Endpoint  string // custom endpoint for MinIO / LocalStack
	S3AccessKey string
	S3SecretKey string
	// GCS
	GCSBucket      string
	GCSPrefix      string
	GCSCredentials string // path to service account JSON or empty for ADC
	// Azure
	AzureAccount          string
	AzureKey              string
	AzureContainer        string
	AzurePrefix           string
	AzureEndpoint         string // override service URL (e.g. Azurite)
	AzureConnectionString string // takes precedence over Account/Key

	// Permissions
	FolderPermissions []FolderPermission
	AutoCreateFolders bool

	// HTTP methods
	ROMethods []string
	RWMethods []string

	// Auth
	BasicAuthEnabled bool
	BasicUsers       map[string]string // username -> plain password (hashed at startup)

	LDAPEnabled bool
	LDAP        LDAPConfig

	OIDCEnabled bool
	OIDC        OIDCConfig

	// Features
	CORSEnabled          bool
	CORSOrigin           string
	CORSAllowedMethods   string
	CORSAllowedHeaders   string
	BrowserAccessBlocked bool
}

// Load reads the configuration from environment variables.
func Load() (*Config, error) {
	cfg := &Config{
		ServerPort:    getEnv("SERVER_PORT", "8080"),
		StorageType:   StorageType(strings.ToLower(getEnv("STORAGE_TYPE", "local"))),
		LocalDataPath: getEnv("LOCAL_DATA_PATH", "./webdav-data"),

		S3Bucket:    getEnv("S3_BUCKET", ""),
		S3Region:    getEnv("S3_REGION", "us-east-1"),
		S3Prefix:    getEnv("S3_PREFIX", ""),
		S3Endpoint:  getEnv("S3_ENDPOINT", ""),
		S3AccessKey: getEnv("AWS_ACCESS_KEY_ID", ""),
		S3SecretKey: getEnv("AWS_SECRET_ACCESS_KEY", ""),

		GCSBucket:      getEnv("GCS_BUCKET", ""),
		GCSPrefix:      getEnv("GCS_PREFIX", ""),
		GCSCredentials: getEnv("GOOGLE_APPLICATION_CREDENTIALS", ""),

		AzureAccount:          getEnv("AZURE_STORAGE_ACCOUNT", ""),
		AzureKey:              getEnv("AZURE_STORAGE_KEY", ""),
		AzureContainer:        getEnv("AZURE_CONTAINER", ""),
		AzurePrefix:           getEnv("AZURE_PREFIX", ""),
		AzureEndpoint:         getEnv("AZURE_STORAGE_ENDPOINT", ""),
		AzureConnectionString: getEnv("AZURE_STORAGE_CONNECTION_STRING", ""),

		AutoCreateFolders: getBoolEnv("AUTO_CREATE_FOLDERS", true),

		ROMethods: splitMethods(getEnv("RO_METHODS", "GET HEAD OPTIONS PROPFIND")),
		RWMethods: splitMethods(getEnv("RW_METHODS", "GET HEAD OPTIONS PROPFIND PUT DELETE MKCOL COPY MOVE LOCK UNLOCK PROPPATCH")),

		BasicAuthEnabled: getBoolEnv("BASIC_AUTH_ENABLED", true),
		BasicUsers:       make(map[string]string),

		LDAPEnabled: getBoolEnv("LDAP_ENABLED", false),
		LDAP: LDAPConfig{
			URL:          getEnv("LDAP_URL", ""),
			BaseDN:       getEnv("LDAP_BASE_DN", ""),
			BindDN:       getEnv("LDAP_BIND_DN", ""),
			BindPassword: getEnv("LDAP_BIND_PASSWORD", ""),
			Attribute:    getEnv("LDAP_ATTRIBUTE", "uid"),
			StartTLS:     getBoolEnv("LDAP_STARTTLS", false),
		},

		OIDCEnabled: getBoolEnv("OAUTH_ENABLED", false),
		OIDC: OIDCConfig{
			ProviderURL:   getEnv("OIDC_PROVIDER_URL", getEnv("OIDCProviderMetadataURL", "")),
			ClientID:      getEnv("OIDC_CLIENT_ID", getEnv("OIDCClientID", "")),
			ClientSecret:  getEnv("OIDC_CLIENT_SECRET", getEnv("OIDCClientSecret", "")),
			RedirectURL:   getEnv("OIDC_REDIRECT_URL", getEnv("OIDCRedirectURI", "")),
			Scopes:        strings.Fields(getEnv("OIDC_SCOPES", "openid email profile")),
			UsernameClaim: getEnv("OIDC_USERNAME_CLAIM", getEnv("OIDCRemoteUserClaim", "preferred_username")),
		},

		CORSEnabled:          getBoolEnv("CORS_ENABLED", false),
		CORSOrigin:           getEnv("CORS_ORIGIN", "*"),
		CORSAllowedMethods:   getEnv("CORS_ALLOWED_METHODS", "GET,HEAD,PUT,DELETE,MKCOL,COPY,MOVE,OPTIONS,PROPFIND,PROPPATCH,LOCK,UNLOCK"),
		CORSAllowedHeaders:   getEnv("CORS_ALLOWED_HEADERS", "Authorization,Content-Type,Depth,If,Lock-Token,Overwrite,Timeout,Destination,X-Requested-With"),
		BrowserAccessBlocked: getBoolEnv("BROWSER_ACCESS_BLOCKED", false),
	}

	// Parse BASIC_USERS: "alice:pass1 bob:pass2"
	if raw := getEnv("BASIC_USERS", ""); raw != "" {
		for _, entry := range strings.Fields(raw) {
			parts := strings.SplitN(entry, ":", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid BASIC_USERS entry %q: expected user:pass", entry)
			}
			cfg.BasicUsers[parts[0]] = parts[1]
		}
	}

	// Parse FOLDER_PERMISSIONS: "/public:public:ro,/private:*:rw,/alice:alice:rw"
	rawPerms := getEnv("FOLDER_PERMISSIONS", "/files:*:rw")
	perms, err := parseFolderPermissions(rawPerms)
	if err != nil {
		return nil, fmt.Errorf("invalid FOLDER_PERMISSIONS: %w", err)
	}
	cfg.FolderPermissions = perms

	// Validate storage type
	switch cfg.StorageType {
	case StorageLocal, StorageS3, StorageGCS, StorageAzure:
	default:
		return nil, fmt.Errorf("unknown STORAGE_TYPE %q: must be local, s3, gcs, or azure", cfg.StorageType)
	}

	if cfg.StorageType == StorageS3 && cfg.S3Bucket == "" {
		return nil, fmt.Errorf("S3_BUCKET is required when STORAGE_TYPE=s3")
	}
	if cfg.StorageType == StorageGCS && cfg.GCSBucket == "" {
		return nil, fmt.Errorf("GCS_BUCKET is required when STORAGE_TYPE=gcs")
	}
	if cfg.StorageType == StorageAzure {
		if cfg.AzureContainer == "" {
			return nil, fmt.Errorf("AZURE_CONTAINER is required when STORAGE_TYPE=azure")
		}
		if cfg.AzureConnectionString == "" && (cfg.AzureAccount == "" || cfg.AzureKey == "") {
			return nil, fmt.Errorf("STORAGE_TYPE=azure requires AZURE_STORAGE_CONNECTION_STRING or AZURE_STORAGE_ACCOUNT + AZURE_STORAGE_KEY")
		}
	}

	return cfg, nil
}

// FolderNames returns only the path component of each permission.
func (c *Config) FolderNames() []string {
	names := make([]string, len(c.FolderPermissions))
	for i, p := range c.FolderPermissions {
		names[i] = p.Path
	}
	return names
}

// parseFolderPermissions parses the FOLDER_PERMISSIONS value.
// ParseFolderPermissions parses the FOLDER_PERMISSIONS value.
func ParseFolderPermissions(raw string) ([]FolderPermission, error) {
	return parseFolderPermissions(raw)
}

func parseFolderPermissions(raw string) ([]FolderPermission, error) {
	var out []FolderPermission
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, ":", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("entry %q must be /path:users:mode", entry)
		}
		path := strings.TrimSpace(parts[0])
		userSpec := strings.TrimSpace(parts[1])
		mode := strings.ToLower(strings.TrimSpace(parts[2]))

		if mode != "ro" && mode != "rw" {
			return nil, fmt.Errorf("mode in entry %q must be ro or rw", entry)
		}

		var users, excluded []string
		for _, u := range strings.Fields(userSpec) {
			if strings.HasPrefix(u, "!") {
				excluded = append(excluded, strings.TrimPrefix(u, "!"))
			} else {
				users = append(users, u)
			}
		}
		if len(users) == 0 {
			return nil, fmt.Errorf("entry %q has no users defined", entry)
		}

		out = append(out, FolderPermission{
			Path:     path,
			Users:    users,
			Excluded: excluded,
			Mode:     mode,
		})
	}
	return out, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func getBoolEnv(key string, fallback bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func splitMethods(s string) []string {
	var out []string
	for _, m := range strings.Fields(s) {
		out = append(out, strings.ToUpper(m))
	}
	return out
}
