package main

import (
  "log"
  "os"
  "time"
  "flag"
  "log/syslog"
  "runtime/pprof"
)

var cpuprofile = flag.String("cpuprofile", "", "write cpu profile to file")
var spool_size = flag.Uint64("spool-size", 1024, "Maximum number of events to spool before a flush is forced.")
var idle_timeout = flag.Duration("idle-flush-time", 5 * time.Second, "Maximum time to wait for a full spool before flushing anyway")
var config_file = flag.String("config", "", "The config file to load")
var use_syslog = flag.Bool("log-to-syslog", false, "Log to syslog instead of stdout")
var from_beginning = flag.Bool("from-beginning", false, "Read new files from the beginning, instead of the end")

var appconfig *AppConfig

func main() {
  flag.Parse()

  if *cpuprofile != "" {
    f, err := os.Create(*cpuprofile)
    if err != nil {
        log.Fatal(err)
    }
    pprof.StartCPUProfile(f)
    go func() {
      time.Sleep(60 * time.Second)
      pprof.StopCPUProfile()
      panic("done")
    }()
  }

  config, err := LoadConfig(*config_file)
  if err != nil {
    log.Println(err)
    os.Exit(1)
  }
  appconfig = &config.Lumberjack

  event_chan := make(chan *FileEvent, 16)
  publisher_chan := make(chan []*FileEvent, 1)
  registrar_chan := make(chan []*FileEvent, 1)

  if len(config.Files) == 0 {
    log.Fatalf("No paths given. What files do you want me to watch?\n")
  }

  // The basic model of execution:
  // - prospector: finds files in paths/globs to harvest, starts harvesters
  // - harvester: reads a file, sends events to the spooler
  // - spooler: buffers events until ready to flush to the publisher
  // - publisher: writes to the network, notifies registrar
  // - registrar: records positions of files read
  // Finally, prospector uses the registrar information, on restart, to
  // determine where in each file to resume a harvester.
  
  log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
  if *use_syslog {
    writer, err := syslog.New(syslog.LOG_INFO | syslog.LOG_DAEMON, "lumberjack")
    if err != nil {
      log.Fatalf("Failed to open syslog: %s\n", err)
    }
    log.SetOutput(writer)
  }

  //Ensure there is a register file, if not create one.
  // TODO Verify we can write to the file
  if _, err := os.Stat(appconfig.RegistrarFile); os.IsNotExist(err) {
      log.Print("Creating new registrar file.")
      _, err := os.Create(appconfig.RegistrarFile)
      if err != nil {
        //Fatal if we cant create a regiser file we can correct track files, better to know now.
        log.Fatal("Error creating registrar file: ", err)
      }
  }

  // Prospect the globs/paths given on the command line and launch harvesters
  for _, fileconfig := range config.Files {
    go Prospect(fileconfig, event_chan)
  }

  // Harvesters dump events into the spooler.
  go Spool(event_chan, publisher_chan, *spool_size, *idle_timeout)

  go Publishv1(publisher_chan, registrar_chan, &config.Network)

  // registrar records last acknowledged positions in all files.
  Registrar(registrar_chan)
} /* main */
