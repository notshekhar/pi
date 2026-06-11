function base64urlEncode(bytes: Uint8Array): string {
    let bin = "";
    for (const b of bytes) bin += String.fromCharCode(b);
    return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=/g, "");
}

export async function generatePKCE(): Promise<{ verifier: string; challenge: string }> {
    const v = new Uint8Array(32);
    crypto.getRandomValues(v);
    const verifier = base64urlEncode(v);
    const hash = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier));
    return { verifier, challenge: base64urlEncode(new Uint8Array(hash)) };
}
