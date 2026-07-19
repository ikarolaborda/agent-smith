#include <stddef.h>
#include <stdint.h>

#include <vector>

#include "png.h"

namespace {

constexpr size_t kMaximumOutputBytes = 64U * 1024U * 1024U;

}  // namespace

extern "C" int LLVMFuzzerTestOneInput(const uint8_t* data, size_t size) {
  png_image image = {};
  image.version = PNG_IMAGE_VERSION;
  if (!png_image_begin_read_from_memory(&image, data, size)) {
    return 0;
  }

  // CVE-2025-64720 is in the simplified API's local compositing path. RGB
  // output removes a palette image's alpha channel; a null background asks
  // libpng to composite onto the caller-provided destination row. The public
  // issue records destination component 190 at the failing access, so this
  // known-bug calibration harness initializes the destination accordingly.
  image.format = PNG_FORMAT_RGB;
  const size_t output_size = PNG_IMAGE_SIZE(image);
  if (output_size == 0 || output_size > kMaximumOutputBytes) {
    png_image_free(&image);
    return 0;
  }
  std::vector<png_byte> output(output_size, 190);
  png_image_finish_read(&image, nullptr, output.data(), 0, nullptr);
  return 0;
}
