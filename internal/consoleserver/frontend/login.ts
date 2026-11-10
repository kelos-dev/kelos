{
function requiredElement<T extends Element = HTMLElement>(selector: string): T {
  const element = document.querySelector<T>(selector);
  if (!element) throw new Error(`Missing required element: ${selector}`);
  return element;
}

const form = requiredElement<HTMLFormElement>('#login-form');
const input = requiredElement<HTMLInputElement>('#token');
const error = requiredElement<HTMLElement>('#login-error');
const reveal = requiredElement<HTMLButtonElement>('#reveal');

reveal.addEventListener('click', () => {
  const hidden = input.type === 'password';
  input.type = hidden ? 'text' : 'password';
  reveal.textContent = hidden ? 'Hide' : 'Show';
});

form.addEventListener('submit', async (event) => {
  event.preventDefault();
  error.textContent = '';
  const button = requiredElement<HTMLButtonElement>('#login-form button[type="submit"]');
  button.disabled = true;
  try {
    const response = await fetch('/api/login', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({token: input.value}),
    });
    if (!response.ok) {
      const body = await response.json().catch(() => ({})) as {error?: string};
      throw new Error(body.error || 'Sign-in failed');
    }
    const query = new URLSearchParams(window.location.search);
    const next = query.get('next') || '';
    const path = /^\/namespaces\/[a-z0-9.-]+\/(?:sessions(?:\/[a-z0-9.-]+)?|resources(?:\/[a-z]+\/[a-z0-9.-]+)?)\/?$/.test(next) ? next : '/';
    query.delete('next');
    const search = query.toString();
    window.location.replace(`${path}${search ? `?${search}` : ''}`);
  } catch (cause) {
    error.textContent = cause instanceof Error ? cause.message : String(cause);
    input.focus();
    input.select();
  } finally {
    button.disabled = false;
  }
});
}
