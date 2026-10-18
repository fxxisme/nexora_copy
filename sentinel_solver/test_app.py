import base64
import json
import shutil
import subprocess
import unittest

from sentinel_solver.app import _SOLVE_VIA_SDK, invalid_turnstile_reason


class InvalidTurnstileReasonTest(unittest.TestCase):
    def test_rejects_invalid_solver_results(self) -> None:
        p = "requirements-token"
        cases = (
            "",
            p,
            "12345",
            base64.b64encode(b"123: timeout").decode("ascii"),
        )
        for token in cases:
            with self.subTest(token=token):
                self.assertTrue(invalid_turnstile_reason(token, p))

    def test_accepts_opaque_token(self) -> None:
        self.assertEqual(invalid_turnstile_reason("opaque-token", "requirements-token"), "")

    @unittest.skipUnless(shutil.which("node"), "Node.js is unavailable")
    def test_sdk_preserves_its_token_cache(self) -> None:
        script = "const solve = eval(" + json.dumps(_SOLVE_VIA_SDK) + ");\n" + """
const source = `
  const cache = new WeakMap();
  const seeds = new WeakMap();
  export const turnstile = {
    tokenCache: cache,
    async getEnforcementToken(requirements) {
      const cached = cache.get(requirements);
      if (cached) return cached;
      const token = 'valid-' + seeds.get(requirements);
      cache.set(requirements, token);
      return token;
    }
  };
`;
const asset = 'data:text/javascript,' + encodeURIComponent(source);
const result = await solve([{}, 'seed', asset]);
if (result !== 'valid-seed') throw new Error('SDK returned ' + result);
"""
        result = subprocess.run(
            ["node", "--input-type=module", "-e", script],
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
