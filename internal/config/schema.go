package config

var allowedKeys = map[string]struct{}{
	"root": {}, "audit_log": {}, "host": {}, "port": {}, "public_hosts": {},
	"cloudflare_access_team_domain": {}, "cloudflare_access_audience": {},
	"artifact_base_url": {}, "preview_base_domain": {}, "preview_access_audience": {},
	"max_file_bytes": {}, "max_write_bytes": {}, "max_output_bytes": {},
	"max_list_entries": {}, "max_search_results": {}, "max_read_lines": {},
	"max_patch_bytes": {}, "max_patch_files": {},
	"github_app_id": {}, "github_installations": {}, "github_api_version": {},
	"github_max_response_bytes": {}, "github_max_pages": {},
	"github_command_timeout_seconds": {}, "github_max_input_bytes": {},
	"github_max_output_bytes": {},
}
