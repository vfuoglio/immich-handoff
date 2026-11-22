---
title: "Preguntas que se hacen primero"
tip: "Respuestas cortas a lo que se pregunta antes de empezar."
source_rev: 9486e154
review: needed
---

> [!NOTE]
> Esta página se ha traducido automáticamente y todavía no la ha revisado una persona. Las guías enlazadas siguen en inglés mientras no estén traducidas.

# Preguntas que se hacen primero

Respuestas cortas, y dónde leer más.

## Lo básico

**¿Es lo mismo que los álbumes compartidos o los enlaces para compartir de Immich?**
No. Esos dejan que la gente *mire* tus fotos. Handoff pone una foto **en otra biblioteca**, recoge fotos de gente sin cuenta y conecta dos servidores. Consulta la [comparación](../../start/why-handoff.md).

**¿La foto se mueve de verdad a la biblioteca de la otra persona?**
Sí. Llega una copia a su Immich como foto suya, con su nombre, fechas, marca de favorita, descripción, valoración y ubicación. [Qué se traslada](../../reference/what-is-carried-over.md) lista lo que viaja y lo que no.

**¿Qué necesito?**
Immich 3.2.0 o posterior, Docker y las etiquetas activadas para todas las personas que participan. Un contenedor pequeño, sin servidor de base de datos. Consulta [Instalación](../../admin/install.md).

**¿Quienes envían tienen que instalar algo?**
No. Usan Immich como hasta ahora: añadir una etiqueta, o pulsar el botón **Entregar…** si tu administrador lo [ha añadido](../../admin/find-handoff.md). Los invitados sin cuenta usan un enlace o un código QR en su navegador.

**¿Funciona con la aplicación móvil de Immich?**
Etiquetar en la aplicación funciona. El botón dentro de Immich funciona en un navegador, no en la aplicación nativa. La página también envía fotos desde un móvil: consulta [Enviar fotos desde un móvil](../../guides/collect/phone.md).

## Tus fotos y tus claves

**¿Qué pasa con mi original?**
Lo eliges al unirte a un servicio: **conservarlo** (se envía una copia) o **moverlo** a tu papelera, lo que solo ocurre cuando el otro lado tiene una copia verificada. El receptor no puede decidirlo por ti.

**¿Tengo que darle a alguien mi contraseña o una clave?**
No. La aplicación crea una clave con nombre y restringida en *tu* cuenta con tu propio inicio de sesión, la guarda cifrada y nadie la ve. Bórrala en Immich, o pulsa **Salir**, y todo se detiene sin tocar una foto. Consulta [Seguridad](../../reference/security.md).

**¿Es oficial?**
No. Es un proyecto comunitario independiente, no hecho ni respaldado por el equipo de Immich.

## Qué más puede hacer

**¿Recoger fotos de gente sin cuenta de Immich, para una boda o una fiesta?**
Sí. Un [buzón de entrega](../../guides/collect/events.md) es un enlace o código QR solo de escritura: aprueba las fotos antes, muéstralas en un muro, da a los invitados una galería con corazones, haz una selección de lo mejor.

**¿Entregar una sesión a clientes?**
Sí. Una [sala de cliente](../../guides/collect/client-rooms.md) muestra un álbum a través de imágenes marcadas, el cliente elige favoritas y tú liberas los originales cuando decidas.

**¿Compartir con alguien en otro servidor Immich?**
Sí, con los [álbumes compartidos entre servidores](../../guides/share/shared-albums.md). Tu amigo ni siquiera necesita Handoff si le das una clave con una lista corta de permisos.

**¿Compartir las fotos de una persona con mi pareja, en ambos sentidos?**
Sí. En los **Ajustes**, **Ofrecer compartir fotos** crea un reparto privado entre dos personas del servidor: cada una elige las personas cuyas fotos van a la otra, la otra dice que sí en una página propia, y esas fotos se ofrecen con imágenes (o se envían solas, si lo activas). No es instantáneo, cada biblioteca guarda su propia copia y aún no reconoce mascotas. Consulta [Compartir las fotos de una persona](../../guides/share/share-people.md).

**¿Conservar una foto que alguien compartió conmigo?**
Sí. El botón dentro de Immich muestra **Añadir a mi biblioteca** en fotos de álbumes compartidos contigo y de una pareja. Consulta [Añadir a mi biblioteca](../../admin/find-handoff.md), o [sigue el álbum](../../guides/share/follow-album.md) para que se te ofrezcan las fotos nuevas según se añaden.

**¿Enviar fotos a un NAS, Nextcloud, S3 o SFTP, en lugar de o además de otra cuenta de Immich?**
Sí. El administrador nombra los lugares en un archivo y el propietario de un servicio añade uno. La foto se escribe, se comprueba, y solo entonces se mueve o marca el original; si un lugar no responde, el original sigue etiquetado hasta que lo tenga. Consulta [Destinos](../../guides/share/destinations.md).

**¿Apilar mis archivos RAW y JPEG, ediciones y ráfagas?**
Sí, con tu visto bueno: **Apilar las fotos que van juntas** ofrece cada pila, nunca cambia ni borra una foto y puede deshacer sus propias pilas durante 15 minutos. Consulta [Apilado](../../guides/automate/stacking.md).

**¿Hacer álbumes a partir de las carpetas de mi biblioteca externa?**
Sí: **Álbumes de mis carpetas** ofrece un álbum por carpeta y pone las fotos cuando dices que sí. Solo añade. Consulta [Álbumes de carpetas](../../guides/automate/folder-albums.md).

**¿Dar a las fotos de una cámara sin GPS el lugar donde se tomaron?**
Sí, con cuidado: **Lugares para fotos que no tienen** ofrece el lugar de una foto tomada minutos antes o después por la misma cámara, y no escribe nada hasta que digas que sí. La aplicación no puede deshacer un lugar escrito (Immich no lo permite). Consulta [Lugares](../../guides/automate/places.md).

**¿Hacer un resumen para compartir de un viaje o un año?**
Sí: las [Historias](../../guides/show/stories.md) convierten un álbum, una persona, un periodo o un buzón de entrega en un breve resumen animado con un enlace que cualquiera puede abrir sin cuenta.

**¿Mostrar fotos en mi tele con un código?**
Sí: abre `/tv` en el navegador de la tele y escribe su código en **Enviar a una tele**. Consulta [Enviar a una tele](../../guides/show/cast-to-tv.md).

**¿Ver mis álbumes como carpetas, como Viajes / 2024 / Roma?**
Sí, en una página aparte: nombra los álbumes con barras y abre **Árbol de álbumes**. Consulta [Árbol de álbumes](../../guides/show/album-tree.md).

**¿Recibir un mensaje los días en que Immich tiene recuerdos para mí?**
Sí: [En este día](../../guides/show/memories.md) envía un mensaje al día, por notificación o correo, cuando hay recuerdos que aún no has visto.

## El futuro

**¿Y si Immich cambia?**
Las pruebas del proyecto se ejecutan contra un Immich real, y el botón dentro de Immich se comprueba contra sus páginas en la versión fijada, así que un cambio se nota cuando se sube esa versión. Si el botón no encuentra su sitio, se oculta solo y nada más se rompe.

**¿Y si Immich lo añade por su cuenta?**
Puede añadir una parte. Handoff solo usa la API pública de Immich y no cambia nada dentro; si Immich cubre una parte de lo que usas, puedes dejar de usar esa parte y nada más se ve afectado. Lo que seguiría siendo útil por sí solo: elegir si el original se queda o se mueve (y que no se quite nada antes de que exista una copia verificada), invitados sin cuenta que envían por un código QR con moderación, un muro y una galería, salas de cliente para fotógrafos y el reparto entre dos servidores. Irse es fácil: borra la clave en Immich, o pulsa **Salir**, y cada foto se queda donde está.

## Qué sigue

- Entregar una primera foto: [Inicio rápido](../start/quickstart.md)
- Elegir un objetivo: [Uso](../using.md)
- Algo no funciona: [Solución de problemas](../../reference/troubleshooting.md)
