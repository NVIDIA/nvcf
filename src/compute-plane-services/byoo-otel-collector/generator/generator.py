# SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import argparse
from pathlib import Path
from typing import Any, Dict, List

from markdown_helper import MarkdownBuilder
from template_helper import TemplateBuilder
import yaml

# ------------------------------
# Config loading
# ------------------------------


class ConfigLoader:
    """Loads configuration from a YAML file."""

    def __init__(self, path: Path):
        """Initializes the ConfigLoader.

        Args:
            path: Path to the YAML configuration file.
        """
        self._path = path

    def load(self) -> Dict[str, Any]:
        """Loads the YAML file and returns the parsed data."""
        if not self._path.exists():
            raise FileNotFoundError(f"Config file does not exist: {self._path}")
        with self._path.open("r", encoding="utf-8") as file:
            return yaml.safe_load(file)


# ------------------------------
# Command-line interface
# ------------------------------


class GeneratorCLI:
    """Parses CLI arguments and orchestrates markdown generation."""

    DEFAULT_CONFIG = Path(__file__).resolve().parent / "source-config.yaml"

    def __init__(self, argv: List[str] | None = None):
        self._parser = argparse.ArgumentParser(
            description="Generate a README markdown and config templates from source-config.yaml",
        )
        self._parser.add_argument(
            "-do",
            "--document-output-dir",
            type=Path,
            required=True,
            help="Output directory for the generated README.md",
        )
        self._parser.add_argument(
            "-to",
            "--template-output-dir",
            type=Path,
            required=True,
            help="Output directory for the generated config templates",
        ) 
        self._parser.add_argument(
            "-c",
            "--config",
            type=Path,
            default=self.DEFAULT_CONFIG,
            help=f"YAML config path (default: {self.DEFAULT_CONFIG})",
        )
        self._parser.add_argument(
            "-df",
            "--document-filename",
            type=str,
            default="README.md",
            help="Document filename (default: README.md)",
        )
        self._parser.add_argument(
            "-st",
            "--source-template-folder",
            type=str,
            default="../internal/otelconfig/source_templates",
            help="Source template folder (default: ../internal/otelconfig/source_templates)",
        )
        self._argv = argv

    def run(self) -> None:
        """Executes the CLI workflow."""
        args = self._parser.parse_args(self._argv)

        document_output_dir: Path = args.document_output_dir
        template_output_dir: Path = args.template_output_dir
        config_path: Path = args.config

        document_output_dir.mkdir(parents=True, exist_ok=True)
        template_output_dir.mkdir(parents=True, exist_ok=True)

        config_loader = ConfigLoader(config_path)
        yaml_data = config_loader.load()

        # Generate markdown document
        markdown = MarkdownBuilder().build(yaml_data)
        readme_path = document_output_dir / args.document_filename
        readme_path.write_text(markdown, encoding="utf-8")
        print(f"Document file generated at: {readme_path}")

        # Generate config templates
        template_builder = TemplateBuilder(config_path, args.source_template_folder, template_output_dir)
        template_builder.build()


# ------------------------------
# Entrypoint
# ------------------------------


def main(argv: List[str] | None = None) -> None:
    """Module entrypoint used by `python -m` or direct execution."""
    GeneratorCLI(argv).run()


if __name__ == "__main__":
    main()
