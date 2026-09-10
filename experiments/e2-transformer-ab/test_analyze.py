import unittest

import numpy as np
from PIL import Image

from analyze import metrics


class MetricContract(unittest.TestCase):
    def test_identical_pixels(self):
        image = Image.new("RGBA", (32, 32), (80, 120, 200, 255))
        result = metrics(image, image, False)
        self.assertEqual(result["ssim_black"], 1.0)
        self.assertEqual(result["mse_white"], 0.0)
        self.assertIsNone(result["psnr_white"])
        self.assertEqual(result["alpha_mae"], 0.0)

    def test_alpha_is_not_ignored(self):
        reference = Image.new("RGBA", (32, 32), (255, 0, 0, 128))
        opaque = Image.new("RGBA", (32, 32), (255, 0, 0, 255))
        result = metrics(reference, opaque, False)
        self.assertEqual(result["alpha_mae"], 127.0)
        self.assertGreater(result["mse_black"], 1000)
        self.assertGreater(result["mse_white"], 1000)

    def test_jpeg_reference_uses_white(self):
        reference = Image.new("RGBA", (32, 32), (255, 0, 0, 128))
        flattened = Image.new("RGBA", (32, 32), (255, 127, 127, 255))
        result = metrics(reference, flattened, True)
        self.assertLess(result["mse_white"], 1e-8)
        self.assertEqual(result["alpha_mae"], 0.0)
        self.assertTrue(np.isclose(result["ssim_white"], 1.0))


if __name__ == "__main__":
    unittest.main()
