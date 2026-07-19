#include <cstddef>
#include <cstdint>
#include <cstdlib>

extern "C" int LLVMFuzzerTestOneInput(const uint8_t *data, size_t size) {
  if (size < 5 || data[0] != 'S' || data[1] != 'M' || data[2] != 'I' ||
      data[3] != 'T') {
    return 0;
  }

  volatile uint8_t *buffer = static_cast<uint8_t *>(std::malloc(8));
  if (buffer == nullptr) {
    return 0;
  }
  const size_t attacker_influenced_offset = 8 + (data[4] & 0x0f);
  buffer[attacker_influenced_offset] = 0x41;
  std::free(const_cast<uint8_t *>(buffer));
  return 0;
}
