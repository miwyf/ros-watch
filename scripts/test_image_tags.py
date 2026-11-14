import os
from pathlib import Path
import subprocess
import unittest


class ImageTagTests(unittest.TestCase):
    image = "ghcr.io/miwyf/ros-watch"

    def check_tags(self, ref, tags, expected):
        result = subprocess.run(
            ["sh", str(Path(__file__).with_name("check_image_tags.sh"))],
            env={**os.environ, "GITHUB_REF": ref, "APP_VERSION": "0.7.13",
                 "IMAGE": self.image, "PUBLISH_TAGS": "\n".join(self.image + ":" + tag for tag in tags)},
            capture_output=True, text=True,
        )
        self.assertEqual(result.returncode == 0, expected, result.stderr)

    def test_dev_only_publishes_dev(self):
        self.check_tags("refs/heads/dev", ["dev"], True)
        for tags in [["dev", "latest"], ["dev", "0.7.13"], ["dev", "sha-commit"], ["latest"], []]:
            with self.subTest(tags=tags):
                self.check_tags("refs/heads/dev", tags, False)

    def test_main_requires_version_and_latest(self):
        self.check_tags("refs/heads/main", ["latest", "0.7.13"], True)
        self.check_tags("refs/heads/main", ["0.7.13", "latest"], True)
        for tags in [["latest"], ["0.7.13"], ["dev"], ["latest", "0.7.13", "dev"]]:
            with self.subTest(tags=tags):
                self.check_tags("refs/heads/main", tags, False)

    def test_release_tags_must_match_source_version(self):
        self.check_tags("refs/tags/v0.7.13", ["0.7.13"], True)
        self.check_tags("refs/tags/v0.7.12", ["0.7.12"], False)
        self.check_tags("refs/tags/v0.7.13", ["0.7.13", "dev"], False)

    def test_other_refs_cannot_publish(self):
        for ref in ["refs/heads/feature", "refs/pull/1/merge"]:
            self.check_tags(ref, ["dev"], False)


if __name__ == "__main__":
    unittest.main()
