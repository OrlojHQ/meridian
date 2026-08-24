package main

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/OrlojHQ/meridian/internal/maintenance"
	"github.com/spf13/cobra"
)

func newAdminCommand(config *cliConfig) *cobra.Command {
	admin := &cobra.Command{
		Use:   "admin",
		Short: "Perform offline local maintenance",
		Long:  "Perform offline backup, restore, and artifact maintenance. Stop meridiand before using these commands.",
	}
	admin.AddCommand(
		newBackupCommand(config), newRestoreCommand(config), newCASCommand(config),
		newTranscriptKeyCommand(config),
	)
	return admin
}

func newTranscriptKeyCommand(config *cliConfig) *cobra.Command {
	group := &cobra.Command{Use: "transcript-key", Short: "Manage the offline transcript installation key"}
	var dataDir, activeKeyFile, newKeyFile string
	var yes bool
	rotate := &cobra.Command{
		Use:   "rotate",
		Short: "Atomically rewrap retained Thread keys offline",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if dataDir == "" {
				return errors.New("--data-dir is required")
			}
			if !yes {
				return errors.New("--yes is required; stop meridiand and all maintenance commands first")
			}
			report, err := maintenance.RotateTranscriptKey(
				command.Context(), dataDir, activeKeyFile, newKeyFile,
			)
			if err != nil {
				return err
			}
			if config.json {
				return writeJSON(config.stdout, report)
			}
			_, err = fmt.Fprintf(config.stdout,
				"rotated key-id=%s key-version=%d threads=%d\n",
				report.KeyID, report.KeyVersion, report.ThreadsRewrapped)
			return err
		},
	}
	rotate.Flags().StringVar(&dataDir, "data-dir", "", "offline Meridian data directory")
	rotate.Flags().StringVar(
		&activeKeyFile, "transcript-key-file", "",
		"active restrictive key file (default under data-dir)",
	)
	rotate.Flags().StringVar(
		&newKeyFile, "new-key-file", "",
		"existing restrictive replacement key; omit to generate one",
	)
	rotate.Flags().BoolVar(&yes, "yes", false, "confirm meridiand is stopped")
	group.AddCommand(rotate)
	return group
}

func newBackupCommand(config *cliConfig) *cobra.Command {
	var dataDir, output string
	backup := &cobra.Command{Use: "backup", Short: "Create or verify a local backup"}
	create := &cobra.Command{
		Use:   "create",
		Short: "Create an atomic SQLite and artifact backup",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if dataDir == "" || output == "" {
				return errors.New("--data-dir and --output are required")
			}
			manifest, err := maintenance.CreateBackup(command.Context(), dataDir, output)
			if err != nil {
				return err
			}
			if config.json {
				return writeJSON(config.stdout, manifest)
			}
			_, err = fmt.Fprintf(
				config.stdout, "backup=%s schema=%d files=%d\n",
				filepath.Clean(output), manifest.SchemaVersion, len(manifest.Files),
			)
			return err
		},
	}
	create.Flags().StringVar(&dataDir, "data-dir", "", "offline Meridian data directory")
	create.Flags().StringVar(&output, "output", "", "new backup directory")

	var verifyPath, verifyTranscriptKey string
	var deepVerify bool
	verify := &cobra.Command{
		Use:   "verify",
		Short: "Verify backup manifest, checksums, and schema",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if verifyPath == "" {
				return errors.New("--backup is required")
			}
			if deepVerify && verifyTranscriptKey == "" {
				return errors.New("--deep requires --transcript-key-file")
			}
			var manifest maintenance.Manifest
			var err error
			if deepVerify {
				manifest, err = maintenance.VerifyBackupWithKey(
					command.Context(), verifyPath, verifyTranscriptKey,
				)
			} else {
				manifest, err = maintenance.VerifyBackup(command.Context(), verifyPath)
			}
			if err != nil {
				return err
			}
			if config.json {
				return writeJSON(config.stdout, manifest)
			}
			_, err = fmt.Fprintf(config.stdout, "verified schema=%d files=%d\n", manifest.SchemaVersion, len(manifest.Files))
			return err
		},
	}
	verify.Flags().StringVar(&verifyPath, "backup", "", "backup directory to verify")
	verify.Flags().BoolVar(&deepVerify, "deep", false, "authenticate encrypted transcript content")
	verify.Flags().StringVar(
		&verifyTranscriptKey,
		"transcript-key-file",
		"",
		"separately protected installation transcript key",
	)
	backup.AddCommand(create, verify)
	return backup
}

func newRestoreCommand(config *cliConfig) *cobra.Command {
	var backup, dataDir, transcriptKeyFile string
	var replace, yes bool
	command := &cobra.Command{
		Use:   "restore",
		Short: "Verify and atomically restore a local backup",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if backup == "" || dataDir == "" {
				return errors.New("--backup and --data-dir are required")
			}
			if replace && !yes {
				return errors.New("--replace requires --yes; stop meridiand and back up the existing installation first")
			}
			manifest, err := maintenance.RestoreBackupWithOptions(
				command.Context(),
				backup,
				dataDir,
				maintenance.RestoreOptions{
					Replace:           replace,
					TranscriptKeyFile: transcriptKeyFile,
				},
			)
			if err != nil {
				return err
			}
			if config.json {
				return writeJSON(config.stdout, manifest)
			}
			_, err = fmt.Fprintf(config.stdout, "restored schema=%d files=%d\n", manifest.SchemaVersion, len(manifest.Files))
			return err
		},
	}
	command.Flags().StringVar(&backup, "backup", "", "verified backup directory")
	command.Flags().StringVar(&dataDir, "data-dir", "", "offline destination data directory")
	command.Flags().BoolVar(&replace, "replace", false, "atomically replace a non-empty destination")
	command.Flags().BoolVar(&yes, "yes", false, "confirm replacement of an existing installation")
	command.Flags().StringVar(
		&transcriptKeyFile,
		"transcript-key-file",
		"",
		"separately protected installation transcript key required by encrypted transcripts",
	)
	return command
}

func newCASCommand(config *cliConfig) *cobra.Command {
	cas := &cobra.Command{Use: "cas", Short: "Verify or garbage-collect local artifacts"}
	var verifyDataDir string
	verify := &cobra.Command{
		Use:   "verify",
		Short: "Verify visible Moment references and artifact digests",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if verifyDataDir == "" {
				return errors.New("--data-dir is required")
			}
			report, err := maintenance.VerifyCAS(command.Context(), verifyDataDir, false)
			if err != nil {
				return err
			}
			return writeCASReport(config, report)
		},
	}
	verify.Flags().StringVar(&verifyDataDir, "data-dir", "", "offline Meridian data directory")

	var gcDataDir string
	var deleteEligible, yes bool
	gc := &cobra.Command{
		Use:   "gc",
		Short: "Report unreachable artifacts; deletion is explicit",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if gcDataDir == "" {
				return errors.New("--data-dir is required")
			}
			if deleteEligible && !yes {
				return errors.New("--delete requires --yes; stop meridiand and create a verified backup first")
			}
			report, err := maintenance.VerifyCAS(command.Context(), gcDataDir, deleteEligible)
			if err != nil {
				return err
			}
			return writeCASReport(config, report)
		},
	}
	gc.Flags().StringVar(&gcDataDir, "data-dir", "", "offline Meridian data directory")
	gc.Flags().BoolVar(&deleteEligible, "delete", false, "delete verified blobs not referenced by visible Moments")
	gc.Flags().BoolVar(&yes, "yes", false, "confirm artifact deletion")
	cas.AddCommand(verify, gc)
	return cas
}

func writeCASReport(config *cliConfig, report maintenance.CASReport) error {
	if config.json {
		return writeJSON(config.stdout, report)
	}
	_, err := fmt.Fprintf(
		config.stdout,
		"referenced=%d stored=%d eligible=%d eligible-bytes=%d deleted=%d deleted-bytes=%d\n",
		report.ReferencedBlobs, report.StoredBlobs, report.EligibleBlobs, report.EligibleBytes,
		report.DeletedBlobs, report.DeletedBytes,
	)
	return err
}
