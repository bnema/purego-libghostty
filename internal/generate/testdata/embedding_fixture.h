#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define GHOSTTY_SUCCESS 0
#define GHOSTTY_API __attribute__((visibility("default")))

typedef void* ghostty_app_t;
typedef enum { GHOSTTY_MODE_A, GHOSTTY_MODE_B = 4 } ghostty_mode_e;
typedef enum { GHOSTTY_NESTED_ONLY } ghostty_nested_only_e;
typedef union {
  uint32_t codepoint;
  ghostty_app_t app;
  struct { const char* name; ghostty_nested_only_e kind; } named;
} ghostty_value_u;
typedef struct {
  ghostty_mode_e mode;
  ghostty_value_u value;
  const char* text;
  size_t text_len;
  bool enabled;
} ghostty_fixture_s;
typedef bool (*ghostty_callback_t)(ghostty_app_t, ghostty_fixture_s);
typedef union { uint32_t value; } ghostty_override_only_u;
GHOSTTY_API ghostty_fixture_s ghostty_fixture(ghostty_app_t, ghostty_callback_t);
#ifdef __APPLE__
GHOSTTY_API void ghostty_apple_only(void);
#endif
#if defined(__APPLE__)
GHOSTTY_API void ghostty_defined_apple_only(void);
#endif
#if defined __APPLE__
GHOSTTY_API void ghostty_defined_space_apple_only(void);
#endif
