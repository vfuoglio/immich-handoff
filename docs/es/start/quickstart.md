---
title: "Inicio rápido: tu primera entrega en diez minutos"
tip: "De cero a la primera entrega en diez minutos."
source_rev: "0d9741e9"
review: needed
---

> [!NOTE]
> Esta página se ha traducido automáticamente y todavía no la ha revisado una persona. Las imágenes muestran la interfaz en inglés. En caso de duda, vale la versión en inglés.

# Inicio rápido: tu primera entrega en diez minutos

Una foto que etiquetas en la aplicación de Immich que ya usas aparece en la biblioteca de otra persona, con una copia verificada antes de que tu original se mueva.

![Etiquetar una foto, Immich se lo dice a immich-handoff, la foto llega a la biblioteca del receptor](../../images/flow.svg)

**Lo que necesitas:** Immich 3.2.0 o posterior, Docker en la máquina que lo ejecuta y unos diez minutos. Si quieres probarlo antes por tu cuenta, [ejecuta la demo](../../../demo/README.md): un comando, fotos de ejemplo, nada tuyo se toca.

## 1. Arrancar la aplicación

En Linux (en un Mac usa `sed -i ''`):

```
git clone --branch v1.4.0 https://github.com/vfuoglio/immich-handoff.git && cd immich-handoff
cp .env.example .env
sed -i -e "s/^WEBHOOK_SECRET=.*/WEBHOOK_SECRET=$(openssl rand -hex 24)/" -e "s/^HANDOFF_STORE_KEY=.*/HANDOFF_STORE_KEY=$(openssl rand -hex 24)/" .env
docker compose up -d
```

> [!TIP]
> Si tu red de Immich no se llama `immich_default`, define antes `IMMICH_NETWORK` en `.env`. **Guarda una copia de `HANDOFF_STORE_KEY`**: cifra las claves que la aplicación guarda de quienes se unen.

## 2. Iniciar sesión y ofrecer un servicio

Abre `http://<tu-host>:8080/` en el mismo nombre de host que usas para Immich e inicia sesión con tu cuenta de Immich. Elige **Ofrecer un servicio propio**, ponle nombre (por ejemplo «Vacaciones 2026»), elige la etiqueta que usarán los remitentes (por ejemplo `share`) y quién puede enviar. Nadie crea una clave de API a mano: la aplicación crea una restringida en cada cuenta en el momento en que alguien se une, y se puede borrar en Immich en cualquier momento.

![El formulario para ofrecer un servicio](../../images/offer-form.png)

## 3. Dejar que alguien se una

Comparte el enlace de invitación o el código QR del servicio. La otra persona lo abre, inicia sesión con su propia cuenta de Immich y se une, eligiendo ella misma qué pasa con sus originales: **conservarlos** y enviar una copia, o **moverlos** a su papelera cuando tu lado tenga una copia verificada.

![Unirse a un servicio y elegir qué pasa con tus originales](../../images/join-choice.png)

> [!WARNING]
> Elige **conservar** mientras pruebas. **Mover** pone el original en la papelera del remitente tras comprobar la copia.

## 4. Etiquetar una foto

En la aplicación de Immich, añade la etiqueta `share` a una foto. En unos segundos está en la biblioteca del receptor, con la etiqueta `inbox`, y la página la muestra en camino. Si no llega, ejecuta `docker compose exec immich-handoff /immich-handoff doctor`; dice qué falla y cómo arreglarlo.

![Las fotos tal como llegan al Immich del receptor](../../images/immich-family.png)

> [!IMPORTANT]
> Todas las personas que participan necesitan las **etiquetas** activadas en Immich (Ajustes de la cuenta, Funciones, Etiquetas) y deben recargar la página de Immich después. La página lo detecta y ofrece un botón **Activar las etiquetas**.

## Qué sigue

- Dejar de etiquetar a mano: [las reglas](../../guides/automate/rules.md) y [el botón dentro de Immich](../../guides/automate/immich-button.md)
- Recoger fotos de invitados en un evento: [los buzones de entrega](../../guides/collect/events.md)
- Enviar desde tu móvil: [la pestaña Enviar](../../guides/collect/phone.md)
- Orientarse en la página: [la página](the-page.md)
- Ponerlo en marcha en serio: [instalación](../../admin/install.md), luego [operación](../../admin/operations.md)
