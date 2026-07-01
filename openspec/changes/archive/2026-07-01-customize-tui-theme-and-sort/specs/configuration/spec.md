## ADDED Requirements

### Requirement: Configure the terminal UI color theme

The config file SHALL support a `ui.theme` section that overrides the terminal
UI color palette, including the main/accent color and the per-role label colors
(dim, good, warn, cool, bad). Each color SHALL be optional; any unset or empty
color SHALL fall back to the built-in default. The effective theme SHALL be
included in the resolved configuration reported by `k3c config view`.

#### Scenario: Override the accent color

- **WHEN** the config file sets `ui.theme.accent` to a color and the user
  launches `k3c ui`
- **THEN** the UI renders its accent (title, selection, borders) in the
  configured color

#### Scenario: Unset colors fall back to defaults

- **WHEN** the config file sets only some theme colors (or none)
- **THEN** the unset colors use the built-in default palette

#### Scenario: Theme appears in the effective config

- **WHEN** the user runs `k3c config view`
- **THEN** the effective `ui.theme` colors are included in the printed
  configuration
