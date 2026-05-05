# Platform Metrics Documentation Generator

This CLI tool turns a structured YAML specification into a human-readable Markdown document.  


## Requirement 

uv

run uv sync to download the dependecy 
```
uv sync
```


## Command-Line Usage

```text
uv run -m generator -do doc -to gen
```

| Option / Flag                     | Type      | Default                              | Description                                                                                       |
| --------------------------------- | --------- | ------------------------------------ | ------------------------------------------------------------------------------------------------- |
| `-do`, `--document-output-dir` **(required)**  | Path      | –                                    | Target directory where the Markdown file will be written. It will be created if it does not exist.|
| `-to`, `--template-output-dir` **(required)**  | Path      | –                                    | Target directory where the comfig template files will be written. It will be created if it does not exist.|
| `-c`, `--config`                  | Path      | `source-config.yaml`       | YAML file that defines global attributes and metric lists.                                        |
| `-df`, `--document-filename`      | string    | `README.md`                          | Name of the generated file (relative to `--output`).                                              |
| `-h`, `--help`                    | –         | –                                    | Show the built-in help message.                                                                   |

### Quick Example

Generate documentation/config templates using the bundled configuration:

```bash
uv run -m generator -c source-config.yaml -do doc -to gen
```

Generate documentation with a custom config and custom file name:

```bash
uv run -m generator \
  ---document-output-dir doc \
  ---template-output-dir gen \
  --config ./source-config.yaml \
  --document-filename METRICS.md \
  --source-template-folder internal/otelconfig/templates
```