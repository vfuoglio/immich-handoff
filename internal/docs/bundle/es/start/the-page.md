> [!NOTE]
> Esta página se ha traducido automáticamente y todavía no la ha revisado una persona. Las imágenes muestran la interfaz en inglés. En caso de duda, vale la versión en inglés.

# Orientarse en la página

La página tiene cuatro vistas y una barra de búsqueda que hace lo que le pides. Este es el recorrido de cinco minutos: dónde está cada cosa, qué significan las palabras y qué te muestra la escena en vivo.

![El Resumen: lo que te espera, la escena en vivo y los números de la semana](../../images/dashboard-owner.png)

## Antes de nada: las etiquetas

Handoff funciona con las **etiquetas** de Immich, y las etiquetas están **desactivadas por defecto**, para cada persona por separado. Si las tuyas están desactivadas, la página muestra un aviso con un botón **Activar las etiquetas** (también puedes hacerlo en Immich: Ajustes de la cuenta, Funciones, **Etiquetas**). Los remitentes las necesitan para etiquetar fotos, los receptores para ver la etiqueta de la bandeja de entrada y filtrar por ella. Activarlas no basta: **recarga la pestaña o ventana de Immich después**, o las opciones de etiquetas no aparecen (en la aplicación del móvil, ciérrala y ábrela de nuevo).

## Las cuatro vistas

La barra superior tiene cuatro vistas, en una píldora en su centro (en un móvil son una barra en la parte inferior, con un icono cada una). La dirección recuerda la vista (`#services`), así que puedes guardarla como marcador, y la página vuelve a ella tras una acción.

- **Resumen.** La fecha y un saludo, la barra de búsqueda, luego **Requiere tu atención**, la **escena en vivo** y **Esta semana**, una debajo de otra. La tarjeta **Empezar** está al final hasta que termines con ella.
- **Servicios.** Cada servicio al que envías, del que recibes, que ofreces o al que puedes unirte. Si puedes crear algo más que servicios, un interruptor arriba cambia entre tus servicios, buzones de entrega, salas de cliente, marcos y álbumes compartidos.
- **Actividad.** Lo que ha ocurrido, la última ejecución y **Sincronizar ahora**.
- **Ajustes.** Lo que configuras una vez y luego olvidas, en categorías (Notificaciones, Fotos, Compartiendo, Pantallas). La vista solo existe si hay algo activado para ti.

### Requiere tu atención

Todo lo que espera a una persona va primero, lo más urgente arriba, cada cosa con su botón. Cuando nada espera, lo dice.

- **Conexión perdida** y **Eliminado por su propietario**: **Limpiar**, que borra lo que la aplicación creó en tu cuenta de Immich (después puedes unirte de nuevo).
- **Desconectado** (un servicio que ofreces): **Reconectar**.
- **Solicitud**: alguien quiere enviar a un servicio que ofreces. **Aprobar** o **Rechazar**; una solicitud se responde aquí y en ningún otro sitio.
- **En pausa**: un servicio que pausaste, o tu propio envío (**Reanudar**), o un servicio que su propietario pausó (no hay nada que pulsar).
- **Límite**: un remitente ha llegado al límite diario, o tú (consulta [los límites diarios](../../guides/automate/controls.md)).

### Esta semana

Un pequeño gráfico con un punto por día (días UTC, hoy a la derecha) de cuántas fotos se entregaron en los últimos siete días: las fotos que **enviaste** y las que **llegaron a los servicios que ofreces**. Cuenta lo que la aplicación registró de las personas que se unieron desde la página, así que los remitentes configurados en `.env` o en un archivo de rutas no aparecen. Al lado, **Esperando para entregarse** (fotos que etiquetaste y todavía han de salir) y **En tu bandeja de entrada** (lo que llegó y aún no está archivado) se actualizan solos cada vez que termina una entrega.

### Empezar

La tarjeta activa las etiquetas, te ayuda a unirte a un servicio u ofrecer uno y a entregar una primera foto, y sugiere más pasos que encajan con lo que haces (invitar a alguien, una notificación). Cada paso tiene **Muéstrame** y un **?** a su guía, y un anillo muestra cuánto llevas. Desaparece cuando terminas o cuando pulsas **Ocultar esta guía**.

## La escena en vivo

![La escena en vivo: un carril para cada remitente y una puerta a la biblioteca](../../images/live-scene.png)

Justo debajo de la barra de búsqueda, **En vivo** muestra lo que ocurre según ocurre. Cada servicio en el que participas tiene un escenario: un **carril** para cada remitente y una puerta a la biblioteca receptora. Un cuadrado de color brillante recorre el carril del remitente hasta la puerta y se extiende un anillo donde aterriza; cuando eres tú quien envía, un cuadrado con contorno y una flecha sale en cambio de tu puerta. El contador **llegó mientras miras** sube con cada uno. Los cuadrados son solo cuadrados: la escena nunca muestra una foto ni un nombre de archivo, y solo entregas reales, nunca inventadas.

Un carril también dice con palabras en qué estado está, para que el color nunca sea la única señal: **En marcha**, **Pausado por el propietario** o **Pausado por el remitente** (carril discontinuo), **Límite diario alcanzado** con cuántas esperan y hasta cuándo (ámbar), **Conexión perdida** (rojo, con una cruz). Un breve registro bajo los escenarios lista las últimas cosas en una frase cada una, y un lector de pantalla recibe la misma frase.

Se mantiene tranquila: nada se mueve mientras nada ocurre, solo vuelan unos pocos cuadrados a la vez, y con movimiento reducido o una pestaña oculta se omiten los cuadrados y solo cambian las etiquetas, el contador y el registro. Si se corta la conexión con la aplicación, la escena se atenúa y lo dice, y luego se pone al día sola.

![La pestaña Servicios: un servicio al que envías, abierto en su sitio, y uno al que puedes unirte](../../images/services-sender.png)

Cada servicio es una fila: su inicial, nombre, qué es, un estado como En pausa o Límite, y el número que importa (fotos en espera, o en tu bandeja de entrada). Púlsala para abrirla en su sitio y ver sus ajustes: la etiqueta, el álbum, quién envía y cuánto, los límites, el enlace de invitación y el botón de pausa. Lo que no se puede deshacer queda aparte al final de un servicio abierto, en un recuadro discontinuo que dice lo que hace: **Salir** (para un servicio al que envías) y **Eliminar servicio** (para uno que ofreces). Cada pregunta se hace en un diálogo de la propia página, con una frase clara y un botón con el nombre de lo que hace (rojo cuando no se puede deshacer); Esc o Cancelar no cambian nada.

![La pestaña Actividad: una cronología de lo que ha ocurrido, la última ejecución y Sincronizar ahora](../../images/activity.png)

## Hazla tuya

- **Ctrl K** (o **Cmd K** en un Mac, o la lupa de la barra, o la barra de búsqueda del Resumen) abre la **paleta de comandos**: escribe unas letras para saltar a una vista o un servicio, o para hacer lo que puedes hacer (aprobar una solicitud, pausar o reanudar, sincronizar, abrir la ayuda, hacer el recorrido). Solo lista lo que tienes permitido, y pulsa el mismo botón que la página, así que las preguntas se siguen haciendo.

  ![La paleta de comandos con vistas, una solicitud que aprobar y los servicios](../../images/palette.png)

- **Colores.** El icono de la paleta elige el **esquema de color** (**Crepúsculo**, los colores del icono de la aplicación, o **Immich**, azules tranquilos sobre grises casi neutros), el icono de media luna cambia entre claro y oscuro, y el icono del altavoz activa o desactiva el sonido suave y la breve vibración del móvil cuando aterriza una foto (el primer clic o pulsación despierta el sonido, una regla de los navegadores). Lo que elijas se guarda en este navegador y prevalece sobre lo que el administrador haya fijado (`THEME` y `THEME_MODE`, consulta [Ajustes](../../reference/settings.md)).
- **Idioma.** El código junto al globo (en, de, fr, es, it, pt, ru) es el menú de idioma: las páginas siguen el idioma de tu navegador y este elige otro para siempre. Solo se traducen las páginas, no el registro.
- **Teclas.** **A** y **D** responden a la solicitud que espera (los botones llevan su tecla), y **G** y luego **O**, **S**, **A** o **T** va al Resumen, Servicios, Actividad o Ajustes. Se callan mientras escribes o hay un diálogo abierto.
- **En un móvil** puedes añadir la página a la pantalla de inicio para que se abra como una aplicación: consulta [Operación](../../admin/operations.md).
- **Movimiento.** La luz detrás de la página deriva y destella cuando aterriza una foto, las tarjetas se inclinan hacia el ratón y los números ruedan como en un panel de salidas. Todo se omite en una pantalla táctil o con movimiento reducido, y nada se mueve mientras la página está en reposo.
- **Álbumes como carpetas** (un nombre como `Viajes / 2024 / Roma`) tienen una página propia: [Árbol de álbumes](../../guides/show/album-tree.md).
- El pie de página dice qué **versión** se está ejecutando, a las personas que han iniciado sesión en Immich.

## Palabras usadas aquí

| Palabra | Significado |
|---|---|
| **Servicio** | Un lugar con nombre al que se pueden entregar fotos, con una etiqueta. «Vacaciones 2026» con la etiqueta `share` es un servicio. |
| **Receptor** (o **propietario**) | La persona en cuya biblioteca aterrizan las fotos. Crea el servicio y decide quién puede enviar. |
| **Remitente** | Alguien que etiqueta fotos para entregarlas a un servicio. |
| **Etiqueta** | La marca que añades a una foto en Immich para enviarla. Cada servicio tiene la suya. |
| **Bandeja de entrada** | Una etiqueta en todo lo que acaba de llegar, para que el receptor encuentre lo nuevo. |
| **Buzón de entrega** | Un enlace, código QR o dirección de correo a la que cualquiera puede enviar fotos sin cuenta. También puede ser un **evento** con galería, muro y una selección de lo mejor. |
| **Pool** | Un servicio donde todos envían y todos reciben, para un viaje o una fiesta. |
| **Sala de cliente** | Un enlace para que un cliente mire un álbum a través de imágenes marcadas y elija favoritas. |
| **Regla** | Una búsqueda en tu Immich (personas, lugares, fechas, una frase) que sugiere fotos para entregar. |
| **Álbum compartido** | Un álbum que dos personas en servidores distintos mantienen sincronizado. |
| **Clave** | La clave de API restringida que la aplicación crea en tu propia cuenta de Immich cuando te unes, para poder etiquetar y entregar por ti. Nadie la ve, y borrarla en Immich lo detiene todo con seguridad. |

## Conviene saber

- **Nada se elimina sin una copia.** Una foto solo va a la papelera cuando cada receptor tiene una copia verificada, y nunca mientras una conexión esté rota.
- **Una foto que el receptor ya tiene en su papelera no se entrega.** El original se conserva y recibe la etiqueta `share-duplicate` (se sustituye la del servicio), para que veas por qué. Cuando el receptor vacíe su papelera, vuelve a añadir la etiqueta.
- **Copias de seguridad del móvil.** Una foto en tu papelera la omite la copia de seguridad de tu móvil. Cuando Immich vacía la papelera, un móvil que aún tenga la foto puede volver a respaldarla.
- **Una etiqueta que acabas de añadir ha desaparecido.** Immich a veces pierde una etiqueta añadida en los primeros momentos tras subir una foto. Añádela de nuevo.

¿Algo no funciona? Consulta [Solución de problemas](../../reference/troubleshooting.md).

## Cuándo desaparece la etiqueta de la bandeja de entrada

- **Archivada en un álbum:** la etiqueta se quita en unos 15 minutos, no al instante.
- **En ningún álbum:** la etiqueta se quita cuando la foto lleva en la biblioteca del receptor el tiempo de caducidad del servicio (30 días por defecto). El reloj empieza cuando la foto se subió a Immich con la entrega, no cuando se tomó, y editar o reetiquetar la foto no lo reinicia.

**¿Por qué un retraso?** La aplicación se entera de las fotos nuevas porque Immich se lo dice: un workflow se dispara cuando se añade una etiqueta y llama a la aplicación. Immich 3.2.0 no ofrece un evento así para los álbumes (los únicos disparadores son que se cree un elemento, que se extraigan sus metadatos y que se añada una etiqueta), así que la única forma de notarlo es mirar. La aplicación comprueba con un temporizador, y la comprobación es deliberadamente pequeña: una búsqueda de fotos con la etiqueta de bandeja de entrada por receptor, y nada más cuando está vacía.

**¿Más rápido o más lento?** Cambia `CLEANUP_INTERVAL_MINUTES` ([Ajustes](../../reference/settings.md)). Más corto significa más peticiones (pequeñas) a Immich y menos espera; `0` apaga el temporizador y lo deja a la comprobación diaria. Si Immich añade un evento de álbum en una versión futura, esto podrá ser instantáneo.

## Qué sigue

- Entregar tu primera foto: [Inicio rápido](quickstart.md)
- Elegir un objetivo: [Uso](../using.md)
- Buscar algo: [Preguntas que se hacen primero](../reference/faq.md)
