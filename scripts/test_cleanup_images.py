import unittest

from cleanup_images import deletion_plan, manifest_graph, tagged_roots


class ImageCleanupTests(unittest.TestCase):
    def test_dev_image_and_its_architectures_survive_stable_cleanup(self):
        stable, dev, amd64, arm64, old = ["sha256:" + digit * 64 for digit in "12345"]
        graph = {stable: {}, dev: {"manifests": [{"digest": amd64}, {"digest": arm64}]}, amd64: {}, arm64: {}}
        versions = [{"id": index, "name": digest, "metadata": {"container": {"tags": ["dev"] if digest == dev else []}}}
                    for index, digest in enumerate([stable, dev, amd64, arm64, old])]
        keep = manifest_graph(lambda digest: (graph[digest], {}), stable)
        for root in tagged_roots(versions, {"dev"}):
            keep.update(manifest_graph(lambda digest: (graph[digest], {}), root))
        self.assertEqual(deletion_plan(versions, keep), [versions[-1]])
        self.assertEqual(tagged_roots([{"name": old}], {"dev"}), set())

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
