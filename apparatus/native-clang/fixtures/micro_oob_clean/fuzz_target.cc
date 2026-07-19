#include <cstddef>
#include <cstdint>
#include <cstdlib>

extern "C" int LLVMFuzzerTestOneInput(const uint8_t *data, size_t size) {
  if (size < 5 || data[0] != 'S' || data[1] != 'M' || data[2] != 'I' ||
      data[3] != 'T') {
    return 0;
  }

  uint8_t *buffer = static_cast<uint8_t *>(std::malloc(8));
  if (buffer == nullptr) {
    return 0;
  }
  const size_t bounded_offset = data[4] & 0x07;
  buffer[bounded_offset] = 0x41;
  std::free(buffer);
  return 0;
}
