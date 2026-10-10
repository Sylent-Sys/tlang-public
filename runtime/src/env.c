/* Exact-name environment access, authorized from the static manifest request. */
#include "tlang_internal.h"

#include <stdlib.h>

bool tlang_env_name_authorized(tlang_string name);

tlang_string tlang_env_get(tlang_fiber* fib, tlang_string name) {
    char* key;
    const char* value;
    tlang_string result;
    if (!tlang_env_name_authorized(name)) {
        tlang_throw_typed(fib, 403, TLANG_STR("environment variable access denied"),
                          TLANG_STR(TLANG_ERROR_PERMISSION), TLANG_STR("env.permission_denied"));
        return TLANG_STR_NULL;
    }
    key = (char*)tlang_alloc_raw(fib, name.len + 1);
    if (name.len != 0) memcpy(key, name.data, name.len);
    key[name.len] = '\0';
    value = getenv(key);
    if (value == NULL) return TLANG_STR_NULL;
    result.data = value;
    result.len = strlen(value);
    return result;
}
