package model

import "time"

const CurrentConfigSchema = 2

// QuickNote is a short-lived or persistent text object.
type QuickNote struct {
	ID              string     `json:"id"`
	Content         string     `json:"content"`
	ContentRedacted bool       `json:"content_redacted,omitempty"`
	ContentType     string     `json:"content_type"`
	Tags            []string   `json:"tags"`
	Pinned          bool       `json:"pinned"`
	IsBurnAfterRead bool       `json:"is_burn_after_read"`
	ReadCount       int        `json:"read_count"`
	MaxReadCount    int        `json:"max_read_count"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type FileAsset struct {
	ID            string     `json:"id"`
	FileName      string     `json:"file_name"`
	FileSize      int64      `json:"file_size"`
	MIMEType      string     `json:"mime_type"`
	StoragePath   string     `json:"storage_path,omitempty"`
	FileHash      string     `json:"file_hash,omitempty"`
	DownloadCount int        `json:"download_count"`
	IsVideo       bool       `json:"is_video"`
	DownloadURL   string     `json:"download_url"`
	StreamURL     string     `json:"stream_url,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type PadDocument struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	Version   int        `json:"version"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type PeerConfig struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// AppConfig persists only password-derived material. AccessKey is a write-only
// API field and is never serialized to disk.
type AppConfig struct {
	SchemaVersion       int          `json:"schema_version"`
	DataDir             string       `json:"data_dir"`
	NodeID              string       `json:"node_id"`
	NodeName            string       `json:"node_name"`
	Port                int          `json:"port"`
	ListenAddress       string       `json:"listen_address"`
	AllowLAN            bool         `json:"allow_lan"`
	PublicBaseURL       string       `json:"public_base_url,omitempty"`
	AutoOpenBrowser     bool         `json:"auto_open_browser"`
	MaxUploadMB         int64        `json:"max_upload_mb"`
	RetentionDays       int          `json:"retention_days"`
	TrashRetentionDays  int          `json:"trash_retention_days"`
	AllowedOrigins      []string     `json:"allowed_origins,omitempty"`
	Peers               []PeerConfig `json:"peers"`
	AccessKey           string       `json:"access_key,omitempty"`
	AccessKeyHash       string       `json:"access_key_hash,omitempty"`
	AccessKeySalt       string       `json:"access_key_salt,omitempty"`
	AccessKeyIterations int          `json:"access_key_iterations,omitempty"`
	// LegacyAccessKey exists only for one-time migration from v1.2 and is
	// removed on the first successful save.
	LegacyAccessKey string `json:"-"`
	DiscoveryKey    string `json:"discovery_private_key,omitempty"`
	TLSCertFile     string `json:"tls_cert_file,omitempty"`
	TLSKeyFile      string `json:"tls_key_file,omitempty"`
}

type PublicConfig struct {
	DataDir            string       `json:"data_dir"`
	NodeID             string       `json:"node_id"`
	NodeName           string       `json:"node_name"`
	Port               int          `json:"port"`
	ListenAddress      string       `json:"listen_address"`
	AllowLAN           bool         `json:"allow_lan"`
	PublicBaseURL      string       `json:"public_base_url,omitempty"`
	AutoOpenBrowser    bool         `json:"auto_open_browser"`
	MaxUploadMB        int64        `json:"max_upload_mb"`
	RetentionDays      int          `json:"retention_days"`
	TrashRetentionDays int          `json:"trash_retention_days"`
	HasAccessKey       bool         `json:"has_access_key"`
	TLSEnabled         bool         `json:"tls_enabled"`
	AllowedOrigins     []string     `json:"allowed_origins,omitempty"`
	Peers              []PeerConfig `json:"peers"`
}

type DeviceNode struct {
	ID          string    `json:"id"`
	Hostname    string    `json:"hostname"`
	IP          string    `json:"ip"`
	Port        int       `json:"port"`
	URL         string    `json:"url"`
	NetworkType string    `json:"network_type"`
	IsLocal     bool      `json:"is_local"`
	Online      bool      `json:"online"`
	LatencyMS   int64     `json:"latency_ms"`
	LastSeen    time.Time `json:"last_seen"`
}

type AuditEvent struct {
	ID        string                 `json:"id"`
	Action    string                 `json:"action"`
	Object    string                 `json:"object"`
	ObjectID  string                 `json:"object_id,omitempty"`
	Summary   string                 `json:"summary"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	PrevHash  string                 `json:"prev_hash,omitempty"`
	Hash      string                 `json:"hash,omitempty"`
	CreatedAt time.Time              `json:"created_at"`
}

type ShareLink struct {
	ID             string     `json:"id"`
	Token          string     `json:"token,omitempty"`
	ObjectType     string     `json:"object_type"`
	ObjectID       string     `json:"object_id"`
	Name           string     `json:"name"`
	URL            string     `json:"url,omitempty"`
	AccessCount    int        `json:"access_count"`
	MaxAccessCount int        `json:"max_access_count"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
	LastAccessedAt *time.Time `json:"last_accessed_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	Status         string     `json:"status,omitempty"`
}

type TrashItem struct {
	ObjectType string    `json:"object_type"`
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Size       int64     `json:"size,omitempty"`
	DeletedAt  time.Time `json:"deleted_at"`
	CreatedAt  time.Time `json:"created_at"`
}

type DashboardStats struct {
	NotesCount   int   `json:"notes_count"`
	FilesCount   int   `json:"files_count"`
	PadsCount    int   `json:"pads_count"`
	StorageUsed  int64 `json:"storage_used"`
	VideosCount  int   `json:"videos_count"`
	TrashCount   int   `json:"trash_count"`
	ActiveShares int   `json:"active_shares"`
}

type APIResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type BackupFileManifest struct {
	ID          string `json:"id"`
	ArchivePath string `json:"archive_path"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
}

type BackupManifest struct {
	FormatVersion int                  `json:"format_version"`
	AppVersion    string               `json:"app_version"`
	CreatedAt     time.Time            `json:"created_at"`
	StateSHA256   string               `json:"state_sha256"`
	ConfigSHA256  string               `json:"config_sha256"`
	Files         []BackupFileManifest `json:"files"`
}
