package storage

import (
	"github.com/tis24dev/proxsave/internal/logging"
)

// NormalizeGFSRetentionConfig applica le correzioni necessarie alla configurazione
// GFS prima di eseguire la retention. Attualmente:
//   - garantisce che il tier DAILY abbia almeno valore 1 (minimo accettato)
//     se la policy è gfs e RETENTION_DAILY è <= 0.
//   - emette una linea di log per documentare l'aggiustamento.
func NormalizeGFSRetentionConfig(logger *logging.Logger, backendName string, cfg RetentionConfig) RetentionConfig {
	if cfg.Policy != "gfs" {
		return cfg
	}

	effective := cfg
	if effective.Daily <= 0 {
		if logger != nil {
			logger.Info("%s: RETENTION_DAILY is %d or not set, enforcing minimum of 1 daily backup", backendName, cfg.Daily)
		}
		effective.Daily = 1
	}

	return effective
}

