# Changelog

## [1.0.1-pre-alpha] - 2026-11-28

### Added
- Persist each channel's streamer playlist cursor so playback resumes at the saved track after viewer requests finish.
- Add public game details while requiring Twitch authentication for game discussions.
- Add per-feature status metadata and channel-scoped chat translation settings.

### Fixed
- Prevent duplicate translated replies and expose Twitch chat diagnostics to the metrics pipeline.
- Improve channel isolation for music, chat, OAuth sessions, and streamer site data.
