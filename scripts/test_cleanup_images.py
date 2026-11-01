import unittest

from cleanup_images import deletion_plan, manifest_graph


class ImageCleanupTests(unittest.TestCase):
    def test_keep_multiarch_and_attestation_manifests(self):
        root, amd64, arm64, attestation = ["sha256:" + digit * 64 for digit in "1234"]
        graph = {
            root: {"manifests": [{"digest": amd64}, {"digest": arm64}, {"digest": attestation}]},
            amd64: {"config": {"digest": "not-a-package-version"}},
            arm64: {},
            attestation: {"subject": {"digest": amd64}},
        }
        keep = manifest_graph(lambda digest: (graph[digest], {}), root)
        self.assertEqual(keep, set(graph))
        versions = [{"id": index, "name": digest} for index, digest in enumerate(graph)]
        versions.append({"id": 99, "name": "sha256:" + "5" * 64})
        self.assertEqual(deletion_plan(versions, keep), [versions[-1]])

    def test_missing_or_invalid_manifest_aborts(self):
        with self.assertRaises(ValueError):
            manifest_graph(lambda _: ({}, {}), "invalid")
        with self.assertRaises(KeyError):
            manifest_graph(lambda _: ({"manifests": [{}]}, {}), "sha256:" + "1" * 64)


if __name__ == "__main__":
    unittest.main()
